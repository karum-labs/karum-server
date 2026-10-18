// Command gateway runs the Karum API gateway.
//
// The gateway is the single public entrypoint for the Flutter client. It
// geocodes vacancies, orchestrates service-taste and service-ai over gRPC, and
// streams progress over SSE. This binary only owns the HTTP surface and
// graceful shutdown; the pipeline internals live in internal/orchestrator.
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

	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/grpcclient"
	"github.com/karum-labs/karum-server/internal/httpapi"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/persist"
	"github.com/karum-labs/karum-server/internal/places"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mem := orchestrator.NewStore()
	var sql *persist.Store
	postgresMode := "down"
	opened, err := persist.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Warn("postgres unavailable; campaigns stay in memory", "error", err)
	} else {
		sql = opened
		mem = opened.Memory()
		postgresMode = "live"
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
		runner.WithRoutes(routes.Client{Key: cfg.RoutesAPIKey})
	}

	cache := geocode.NewTieredCache(cfg.RedisAddr)
	var geocoder geocode.Geocoder
	if cfg.GeocodeAPIKey != "" {
		geocoder = geocode.GoogleGeocoder{Key: cfg.GeocodeAPIKey, Cache: cache}
	}

	return runtimeDeps{
		sql:     sql,
		clients: clients,
		http: httpapi.Dependencies{
			Live:     runner,
			Fixture:  runner,
			Store:    mem,
			Geocoder: geocoder,
			Async:    true,
			Probes: httpapi.Probes{
				Taste:    httpModeProbe("http://" + cfg.TasteAddr + "/health"),
				AI:       httpModeProbe("http://" + cfg.AIAddr + "/health"),
				Redis:    func(context.Context) string { return redisMode(cache) },
				Postgres: func(context.Context) string { return postgresMode },
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
