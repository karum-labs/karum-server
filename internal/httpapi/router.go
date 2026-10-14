// Package httpapi wires the Gin router and the HTTP handlers for the gateway.
//
// The gateway is the single public entrypoint for the Flutter client. It
// geocodes the vacancy, orchestrates the two microservices over gRPC, and
// streams progress to the client. This package holds the HTTP surface; the
// orchestration internals live in internal/orchestrator.
package httpapi

import (
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
}

// NewRouter builds the Gin engine with all routes registered.
func NewRouter(cfg *config.Config) *gin.Engine {
	// Release mode keeps production logs free of debug noise. Set GIN_MODE=debug
	// locally to get the verbose request log.
	if gin.Mode() == gin.ReleaseMode {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())

	registerRoutes(r, cfg)

	return r
}

// registerRoutes adds every route to the engine.
func registerRoutes(r *gin.Engine, cfg *config.Config) {
	started := time.Now()

	// GET /health reports gateway liveness and the operational mode. It is the
	// single probe Compose, Cloud Run, and the uptime check all point at.
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, HealthResponse{
			Service:       "gateway",
			Status:        "ok",
			Mode:          cfg.Mode,
			Version:       cfg.Version,
			UptimeSeconds: int64(time.Since(started).Seconds()),
			Time:          time.Now().UTC(),
		})
	})

	// API v1 group. The analysis/job endpoints are added in a later pass; the
	// group exists now so the URL shape is stable from the first commit.
	v1 := r.Group("/api/v1")
	{
		// TODO(analysis): POST /analyses creates a job from address, neighbour
		// text, and constraints and returns a job id.
		// TODO(analysis): GET  /analyses/:id returns state + confirmation payload.
		// TODO(analysis): POST /analyses/:id/confirm resumes with chosen entities.
		// TODO(analysis): GET  /analyses/:id/result returns the shortlist.
		// TODO(analysis): POST /analyses/:id/drafts/{memo,outreach,listing}.
		// TODO(analysis): GET  /events is the SSE trace stream keyed by cursor.
		v1.GET("/", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"service": "gateway",
				"api":     "v1",
				"status":  "ok",
			})
		})
	}

	// Catch-all 404 so unknown paths return JSON rather than Gin's default HTML.
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "not_found",
			"message": "no such route",
			"path":    c.Request.URL.Path,
		})
	})
}
