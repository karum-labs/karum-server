package httpapi

import (
	"context"
	"sync"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/pdf"
	"github.com/karum-labs/karum-server/internal/presets"
	"github.com/karum-labs/karum-server/internal/resilience"
)

// Renderer draws the run-sheet and the site brief. A failure here must not
// remove the campaign the screen already has.
type Renderer interface {
	RunSheet(pdf.Document) ([]byte, error)
	SiteBrief(pdf.Document) ([]byte, error)
}

type stockRenderer struct{}

func (stockRenderer) RunSheet(doc pdf.Document) ([]byte, error)  { return pdf.RunSheet(doc) }
func (stockRenderer) SiteBrief(doc pdf.Document) ([]byte, error) { return pdf.SiteBrief(doc) }

// Probes report dependency modes on GET /health. Nil probes are "not_configured".
type Probes struct {
	Taste    func(context.Context) string
	AI       func(context.Context) string
	Redis    func(context.Context) string
	Postgres func(context.Context) string
	QlooMode func() string
}

// Dependencies is everything the HTTP surface needs beyond process config.
type Dependencies struct {
	Live     *orchestrator.Runner
	Fixture  *orchestrator.Runner
	Store    *orchestrator.Store
	Geocoder geocode.Geocoder
	Catalog  *presets.Catalog
	Limiter  *resilience.Limiter
	// LoginLimiter bounds password attempts separately from scout requests.
	LoginLimiter *resilience.Limiter
	Idem         *resilience.Idempotency
	Renderer     Renderer
	Auth         *auth.Service
	Probes       Probes
	// Async starts the pipeline in the background and returns the job id
	// immediately. Tests leave it false so GET sees a finished run.
	Async bool
}

// Handler serves the scout API.
type Handler struct {
	cfg          *config.Config
	live         *orchestrator.Runner
	fixture      *orchestrator.Runner
	store        *orchestrator.Store
	geocoder     geocode.Geocoder
	catalog      *presets.Catalog
	limiter      *resilience.Limiter
	loginLimiter *resilience.Limiter
	idem         *resilience.Idempotency
	renderer     Renderer
	auth         *auth.Service
	probes       Probes
	async        bool
	cancels      cancelRegistry
}

func newHandler(cfg *config.Config, deps Dependencies) *Handler {
	if deps.Catalog == nil {
		deps.Catalog = presets.New()
	}
	if deps.Idem == nil {
		deps.Idem = resilience.NewIdempotency()
	}
	if deps.Limiter == nil {
		deps.Limiter = resilience.NewLimiter(20, 40, 100, 200)
	}
	if deps.LoginLimiter == nil {
		deps.LoginLimiter = resilience.NewLimiter(0.2, 5, 20, 40)
	}
	if deps.Renderer == nil {
		deps.Renderer = stockRenderer{}
	}
	if deps.Store == nil {
		deps.Store = orchestrator.NewStore()
	}
	return &Handler{
		cfg: cfg, live: deps.Live, fixture: deps.Fixture, store: deps.Store,
		geocoder: deps.Geocoder, catalog: deps.Catalog, limiter: deps.Limiter,
		loginLimiter: deps.LoginLimiter,
		idem:         deps.Idem, renderer: deps.Renderer, auth: deps.Auth, probes: deps.Probes, async: deps.Async,
	}
}

type cancelRegistry struct {
	mu sync.Mutex
	fn map[string]context.CancelFunc
}

func (c *cancelRegistry) put(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fn == nil {
		c.fn = map[string]context.CancelFunc{}
	}
	c.fn[id] = cancel
}

func (c *cancelRegistry) cancel(id string) bool {
	c.mu.Lock()
	cancel := c.fn[id]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (c *cancelRegistry) drop(id string) {
	c.mu.Lock()
	delete(c.fn, id)
	c.mu.Unlock()
}

func (h *Handler) runnerFor(freeEntry bool) (*orchestrator.Runner, error) {
	if freeEntry || h.cfg.Mode == config.ModeLive {
		if h.live == nil {
			return nil, errLiveRequired
		}
		return h.live, nil
	}
	if h.fixture != nil {
		return h.fixture, nil
	}
	if h.live != nil {
		return h.live, nil
	}
	return nil, errLiveRequired
}
