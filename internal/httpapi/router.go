// Package httpapi wires the Gin router and the HTTP handlers for the gateway.
//
// The gateway is the single public entrypoint for the Flutter client. It
// geocodes the vacancy, orchestrates the two microservices over gRPC, and
// streams progress to the client. This package holds the HTTP surface; the
// orchestration internals live in internal/orchestrator.
package httpapi

import (
	"context"
	"net/http"
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
	registerRoutes(r, newHandler(cfg, deps))
	return r
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
		v1.POST("/scout", h.postScout)
		v1.GET("/scout/:id", h.getScout)
		v1.GET("/scout/:id/events", h.getEvents)
		v1.POST("/scout/:id/abort", h.postAbort)
		v1.POST("/scout/:id/drafts/run-sheet", h.postRunSheet)
		v1.POST("/scout/:id/drafts/site-brief", h.postSiteBrief)
		v1.POST("/scout/:id/drafts/doors/:doorID", h.postDoorPitch)
		v1.GET("/campaigns/:id", h.getCampaign)
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
