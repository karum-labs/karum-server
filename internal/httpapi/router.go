// Package httpapi wires the Gin router and the HTTP handlers for the gateway.
//
// The gateway is the single public entrypoint for the Flutter client. It
// geocodes the campaign city, orchestrates the two microservices over gRPC, and
// streams progress to the client. This package holds the HTTP surface; the
// orchestration internals live in internal/orchestrator.
package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/config"
)

// HealthResponse is the payload returned by GET /health.
type HealthResponse struct {
	// Service is the stable name of this process.
	Service string `json:"service"`
	// Status is "ok" when the process is serving traffic.
	Status string `json:"status"`
	// Mode is live, fixture, or degraded.
	Mode config.Mode `json:"mode"`
	// Version is the build version.
	Version string `json:"version"`
	// HackathonMode is the gateway-owned flag the login screen reads.
	HackathonMode bool `json:"hackathon_mode"`
	// UptimeSeconds is how long this process has been running.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Time is when this health payload was generated.
	Time time.Time `json:"time"`
	// Dependencies reports gateway, taste, AI, Redis, Postgres, and Qloo mode.
	Dependencies HealthDependencies `json:"dependencies"`
}

// HealthDependencies is the per-dependency mode block on GET /health.
type HealthDependencies struct {
	Gateway  string `json:"gateway"`
	Taste    string `json:"taste"`
	AI       string `json:"ai"`
	Redis    string `json:"redis"`
	Postgres string `json:"postgres"`
	QlooMode string `json:"qloo_mode"`
}

// NewRouter builds the Gin engine with health and the API index. Scout
// routes answer 503 until NewRouterWithDeps wires a pipeline.
func NewRouter(cfg *config.Config) *gin.Engine {
	return NewRouterWithDeps(cfg, Dependencies{})
}

// NewRouterWithDeps builds the engine with the scout pipeline attached.
func NewRouterWithDeps(cfg *config.Config, deps Dependencies) *gin.Engine {
	if gin.Mode() == gin.ReleaseMode {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware(cfg.ClientOrigins))
	registerRoutes(r, newHandler(cfg, deps))
	return r
}

// corsMiddleware allows browser clients from the configured origins. With no
// allow-list it permits local development origins (localhost and 127.0.0.1 on
// any port), which is what `make local` serves the Flutter client from.
func corsMiddleware(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		if originAllowed(origin, allowed) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if requested := c.GetHeader("Access-Control-Request-Headers"); requested != "" {
				c.Header("Access-Control-Allow-Headers", requested)
			} else {
				c.Header("Access-Control-Allow-Headers", "Accept, Content-Type, Idempotency-Key, Authorization")
			}
			c.Header("Access-Control-Max-Age", "600")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// originAllowed matches the request origin against the configured list, or
// accepts a loopback host when no list is configured.
func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		return isLoopbackOrigin(origin)
	}
	for _, candidate := range allowed {
		if origin == candidate {
			return true
		}
	}
	return false
}

// isLoopbackOrigin reports whether origin is http(s)://localhost or
// http(s)://127.0.0.1 on any port.
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
}

// registerRoutes adds every route to the engine.
func registerRoutes(r *gin.Engine, h *Handler) {
	started := time.Now()

	// GET /health reports this process plus taste, AI, Redis, Postgres, and
	// the Qloo mode. Process liveness stays "ok" so a dependency blip does not
	// fail the Compose probe; the dependency block carries the detail.
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, HealthResponse{
			Service:       "gateway",
			Status:        "ok",
			Mode:          h.cfg.Mode,
			Version:       h.cfg.Version,
			HackathonMode: h.cfg.HackathonMode,
			UptimeSeconds: int64(time.Since(started).Seconds()),
			Time:          time.Now().UTC(),
			Dependencies:  h.dependencies(c.Request.Context()),
		})
	})

	v1 := r.Group("/api/v1")
	{
		v1.GET("/", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"service": "gateway",
				"api":     "v1",
				"status":  "ok",
			})
		})
		v1.POST("/auth/login", h.postLogin)
		v1.POST("/auth/register", h.postRegister)
		v1.POST("/auth/judge", h.postJudgeLogin)
		v1.GET("/auth/me", h.requiredAuth(), h.getMe)
		v1.POST("/auth/logout", h.requiredAuth(), h.postLogout)
		v1.GET("/presets", h.getPresets)
		v1.GET("/account/campaigns", h.requiredAuth(), h.getAccountCampaigns)
		v1.POST("/prewarm", h.requiredAuth(), h.postPrewarm)
		v1.POST("/scout", h.optionalAuth(), h.postScout)
		v1.GET("/scout/:id", h.optionalAuth(), h.getScout)
		v1.GET("/scout/:id/events", h.optionalAuth(), h.getEvents)
		v1.POST("/scout/:id/abort", h.optionalAuth(), h.postAbort)
		v1.POST("/scout/:id/drafts/run-sheet", h.optionalAuth(), h.postRunSheet)
		v1.POST("/scout/:id/drafts/site-brief", h.optionalAuth(), h.postSiteBrief)
		v1.POST("/scout/:id/drafts/doors/:doorID", h.optionalAuth(), h.postDoorPitch)
		v1.POST("/scout/:id/drafts/doors/:doorID/export", h.optionalAuth(), h.postPitchExport)
		v1.POST("/scout/:id/drafts/doors/:doorID/contact", h.optionalAuth(), h.postContact)
		v1.GET("/campaigns/:id", h.optionalAuth(), h.getCampaign)
		v1.GET("/places/details", h.getPlaceDetails)
		v1.GET("/places/photo", h.getPlacePhoto)
	}

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "not_found",
			"message": "no such route",
			"path":    c.Request.URL.Path,
		})
	})
}

func (h *Handler) dependencies(ctx context.Context) HealthDependencies {
	return HealthDependencies{
		Gateway:  "ok",
		Taste:    probe(ctx, h.probes.Taste),
		AI:       probe(ctx, h.probes.AI),
		Redis:    probe(ctx, h.probes.Redis),
		Postgres: probe(ctx, h.probes.Postgres),
		QlooMode: qlooMode(h),
	}
}

func probe(ctx context.Context, fn func(context.Context) string) string {
	if fn == nil {
		return "not_configured"
	}
	status := fn(ctx)
	if status == "" {
		return "down"
	}
	return status
}

func qlooMode(h *Handler) string {
	if h.probes.QlooMode != nil {
		return h.probes.QlooMode()
	}
	if h.cfg == nil {
		return "not_configured"
	}
	return string(h.cfg.Mode)
}
