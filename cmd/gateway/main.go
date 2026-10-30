// Command gateway runs the Karum API gateway.
//
// The gateway is the single public entrypoint for the Flutter client. It
// geocodes the campaign city, orchestrates service-taste and service-ai over
// gRPC, and streams progress over SSE. This binary only owns the HTTP surface
// and graceful shutdown; the pipeline internals live in internal/orchestrator.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/grpcclient"
	"github.com/karum-labs/karum-server/internal/httpapi"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/persist"
	"github.com/karum-labs/karum-server/internal/places"
	"github.com/karum-labs/karum-server/internal/presets"
	"github.com/karum-labs/karum-server/internal/resilience"
	"github.com/karum-labs/karum-server/internal/routes"
)

// healthcheckURL is the endpoint the -healthcheck mode probes.
const healthcheckURL = "/health"

func main() {
	// -healthcheck runs a single probe against a locally running gateway and
	// exits 0 on success, non-zero otherwise. It exists so the distroless
	// runtime image — which has no shell or curl — can still satisfy a Compose
	// or Cloud Run healthcheck by invoking the binary itself.
	healthcheck := flag.Bool("healthcheck", false, "probe a running gateway and exit")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	if err := run(); err != nil {
		slog.Error("gateway exited with error", "error", err)
		os.Exit(1)
	}
}

// runHealthcheck performs one GET against the local /health endpoint.
// It returns the process exit code: 0 when healthy, 1 when not.
func runHealthcheck() int {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck config error:", err)
		return 1
	}

	url := fmt.Sprintf("http://127.0.0.1%s%s", cfg.Addr(), healthcheckURL)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck request failed:", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return 0
	}

	fmt.Fprintln(os.Stderr, "healthcheck unexpected status:", resp.Status)
	return 1
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	// Structured JSON logging so Cloud Run and the judging-window dashboard can
	// parse log lines. Credentials are never logged.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting gateway",
		"version", cfg.Version,
		"mode", cfg.Mode,
		"addr", cfg.Addr(),
	)

	deps := buildDeps(cfg)
	defer deps.close()
	router := httpapi.NewRouterWithDeps(cfg, deps.http)

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// serveErr receives the result of the background ListenAndServe call.
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("gateway listening", "addr", cfg.Addr())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// Wait for an interrupt signal or a serve error, then shut down cleanly.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("shutdown signal received", "signal", sig.String())
	case err := <-serveErr:
		if err != nil {
			return err
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return err
	}

	slog.Info("gateway stopped cleanly")
	return nil
}

type runtimeDeps struct {
	http    httpapi.Dependencies
	clients *grpcclient.Clients
	sql     *persist.Store
}

func (d runtimeDeps) close() {
	if d.clients != nil {
		_ = d.clients.Close()
	}
	if d.sql != nil {
		d.sql.Close()
	}
}

func buildDeps(cfg *config.Config) runtimeDeps {
	// One budget covers connect, migrate, seed, and hydrate. Hydrate reads the
	// stored run payloads, which grow with every flagship run (a 2.7 MB row was
	// measured on the shared database), so the 20 s this used to allow was
	// consumed before the judge account could be seeded. A hosted Postgres may
	// also be scaled to zero, so the wake sits inside the same budget. When this
	// expires the gateway falls back to memory with no account system at all.
	//
	// CockroachDB applies DDL as schema-change jobs and took roughly 40-50 s per
	// migration on a multi-region cluster, so the first boot (0001-0003) needs
	// more than the old 90 s. Later boots skip applied migrations and stay fast.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	mem := orchestrator.NewStore()
	var sql *persist.Store
	opened, err := persist.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Warn("postgres unavailable; campaigns stay in memory", "error", err)
	} else {
		sql = opened
		mem = opened.Memory()
	}

	var authService *auth.Service
	if sql != nil {
		authService = auth.NewService(sql, cfg.SessionTTL)
		if err := sql.DeleteExpiredSessions(ctx); err != nil {
			slog.Warn("could not clean expired sessions", "error", err)
		}
		if cfg.JudgeEmail != "" && cfg.JudgePassword != "" {
			ids := make([]string, 0, len(presets.New().All()))
			for _, preset := range presets.New().All() {
				ids = append(ids, presets.CampaignID(preset.ID))
			}
			if _, err := authService.EnsureJudge(ctx, presets.JudgeAccountID, cfg.JudgeEmail, cfg.JudgePassword, cfg.JudgeDisplayName, ids); err != nil {
				slog.Warn("judge account could not be seeded", "error", err)
			} else {
				// Mirror SQL's `LinkCampaigns` write into the in-memory
				// campaign records. Without this the handler would read an
				// unowned preset record and let any visitor see the judge's
				// prior runs through the shared cmp-* ids (PLAN.md §49).
				mem.LinkPresets(presets.JudgeAccountID, ids)
				seedJudgeBrand(ctx, sql)
				slog.Info("judge account ready", "email", cfg.JudgeEmail, "campaigns", len(ids))
			}
		}
	}

	clients, err := grpcclient.Dial(cfg.TasteGRPC, cfg.AIGRPC)
	if err != nil {
		slog.Warn("grpc dial failed", "error", err)
	}
	var taste orchestrator.TasteClient
	var agent orchestrator.AgentClient
	if clients != nil {
		taste = clients
		agent = clients
	}
	runner := orchestrator.NewRunner(taste, agent, persist.Bridge{Mem: mem, SQL: sql}).
		WithDeadlines(cfg.TasteTimeout, cfg.AITimeout, cfg.PlacesTimeout, cfg.RoutesTimeout)
	if clients != nil {
		runner.WithSeedResolver(clients).WithHeatmap(clients).WithCompare(clients)
	}
	if cfg.PlacesAPIKey != "" {
		runner.WithPlaces(places.Client{Key: cfg.PlacesAPIKey})
	}
	if cfg.RoutesAPIKey != "" {
		runner.WithRoutes(routes.Client{
			Key:  cfg.RoutesAPIKey,
			HTTP: routes.NewHTTPClient(20 * time.Second),
		})
	}

	cache := geocode.NewTieredCache(cfg.RedisAddr)
	var geocoder geocode.Geocoder
	if cfg.GeocodeAPIKey != "" {
		geocoder = geocode.GoogleGeocoder{Key: cfg.GeocodeAPIKey, Cache: cache}
	}

	// The create keys live in Postgres so a double submit returns the original
	// job across a restart or another replica; the in-process store is the
	// fallback when the database is unreachable (PLAN.md §26).
	idempotency := &resilience.FallbackStore{Memory: resilience.NewIdempotency()}
	if sql != nil {
		store := persist.NewIdempotency(sql.Pool())
		store.StartSweeper(ctx, time.Hour)
		idempotency.Primary = store
	}

	return runtimeDeps{
		sql:     sql,
		clients: clients,
		http: httpapi.Dependencies{
			Live: runner,
			// The fixture runner is the same pipeline: in fixture mode the
			// Qloo calls are served from the pack by service-taste, and a
			// draft still calls the model. The seam exists so a deployment
			// can point the two modes at different transports.
			Fixture:  runner,
			Store:    mem,
			Geocoder: geocoder,
			Auth:     authService,
			Brands:   brandStore(sql),
			Idem:     idempotency,
			Async:    true,
			Probes: httpapi.Probes{
				Taste: httpModeProbe("http://" + cfg.TasteAddr + "/health"),
				AI:    httpModeProbe("http://" + cfg.AIAddr + "/health"),
				Redis: func(context.Context) string { return redisMode(cache) },
				Postgres: func(ctx context.Context) string {
					if sql == nil {
						return "down"
					}
					pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					if err := sql.Ping(pingCtx); err != nil {
						return "down"
					}
					return "live"
				},
				QlooMode: func() string { return string(cfg.Mode) },
			},
		},
	}
}

func redisMode(cache *geocode.TieredCache) string {
	if cache == nil {
		return "not_configured"
	}
	if cache.Degraded() {
		return "degraded"
	}
	return "live"
}

func httpModeProbe(url string) func(context.Context) string {
	return func(ctx context.Context) string {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "down"
		}
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "down"
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "down"
		}
		var body struct {
			Mode string `json:"mode"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err := json.Unmarshal(raw, &body); err != nil || body.Mode == "" {
			return "degraded"
		}
		return body.Mode
	}
}

// brandStore returns the Postgres brand profiles, or nil so the handler uses
// its in-process map. A nil *persist.Store must not become a non-nil
// interface value.
func brandStore(sql *persist.Store) httpapi.BrandStore {
	if sql == nil {
		return nil
	}
	return sql
}

// judgeBrand is the demo brand the judge account pitches with until the
// judge edits it on the account screen. It carries no figures: the pitch
// writer drops any sentence with a number it cannot cite.
var judgeBrand = orchestrator.BrandContext{
	Name:     "Bloom Soda Co.",
	Category: "Craft soft drinks",
	Description: "Small-batch sparkling sodas made with real fruit, botanicals and cane sugar, " +
		"with no artificial flavours or colours. Sold in glass bottles for the fridge and on " +
		"draft for bars that pour it as a mixer.",
	Positioning: "A grown-up non-alcoholic option and a better cocktail mixer: less sweet than " +
		"mainstream colas, with distinctive flavours that let a venue charge more for alcohol-free " +
		"drinks and give sober-curious guests a reason to order a second round.",
	Audience: "Health-conscious adults, sober-curious drinkers, designated drivers, and cocktail " +
		"bars and restaurants that want a premium mixer and an alcohol-free menu that sells.",
	SKUs: []orchestrator.SKU{
		{ID: "bloom-yuzu-ginger", Name: "Yuzu Ginger Sparkling"},
		{ID: "bloom-blood-orange", Name: "Blood Orange and Hibiscus"},
		{ID: "bloom-smoked-cola", Name: "Smoked Cola"},
		{ID: "bloom-cucumber-tonic", Name: "Cucumber Mint Tonic"},
	},
}

// seedJudgeBrand gives the judge a brand profile once. An existing profile,
// including one the judge edited, is left alone.
func seedJudgeBrand(ctx context.Context, sql *persist.Store) {
	if _, found, err := sql.BrandProfile(ctx, presets.JudgeAccountID); err != nil || found {
		if err != nil {
			slog.Warn("judge brand profile could not be read", "error", err)
		}
		return
	}
	if err := sql.PutBrandProfile(ctx, presets.JudgeAccountID, judgeBrand); err != nil {
		slog.Warn("judge brand profile could not be seeded", "error", err)
	}
}
