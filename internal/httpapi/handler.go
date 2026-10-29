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

// Renderer draws the run-sheet, the site brief, and a single-door pitch PDF.
// A failure here must not remove the campaign the screen already has.
type Renderer interface {
	RunSheet(pdf.Document) ([]byte, error)
	SiteBrief(pdf.Document) ([]byte, error)
	PitchExport(pdf.PitchDocument) ([]byte, error)
}

type stockRenderer struct{}

func (stockRenderer) RunSheet(doc pdf.Document) ([]byte, error)  { return pdf.RunSheet(doc) }
func (stockRenderer) SiteBrief(doc pdf.Document) ([]byte, error) { return pdf.SiteBrief(doc) }
func (stockRenderer) PitchExport(doc pdf.PitchDocument) ([]byte, error) {
	return pdf.PitchExport(doc)
}

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
	// RegenLimiter bounds the expensive per-door regenerate route, the one
	// other path that bills a model call.
	RegenLimiter *resilience.Limiter
	// ContactLimiter bounds send-per-door sends per visitor and per
	// recipient, so a bad loop cannot spam one venue (PLAN.md §67).
	ContactLimiter *resilience.Limiter
	// PlacesLimiter bounds the place-details and photo proxies. Both are
	// public and every call is billed to the gateway's Places key.
	PlacesLimiter *resilience.Limiter
	// PlacesBaseURL points the Places proxies somewhere other than Google.
	// Tests set it; production leaves it empty.
	PlacesBaseURL string
	// Idem remembers the create keys. It is the Postgres-backed store when
	// the database is reachable, with the in-process store as the fallback.
	Idem     resilience.IdempotencyStore
	Renderer Renderer
	Auth     *auth.Service
	Probes   Probes
	// Async starts the pipeline in the background and returns the job id
	// immediately. Tests leave it false so GET sees a finished run.
	Async bool
}

// Handler serves the scout API.
type Handler struct {
	cfg           *config.Config
	live          *orchestrator.Runner
	fixture       *orchestrator.Runner
	store         *orchestrator.Store
	geocoder      geocode.Geocoder
	catalog       *presets.Catalog
	limiter       *resilience.Limiter
	loginLimiter  *resilience.Limiter
	regenLimiter  *resilience.Limiter
	contactLimiter *resilience.Limiter
	placesLimiter *resilience.Limiter
	placesBase    string
	idem          resilience.IdempotencyStore
	renderer      Renderer
	auth          *auth.Service
	probes        Probes
	async         bool
	cancels       cancelRegistry
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
	if deps.RegenLimiter == nil {
		deps.RegenLimiter = resilience.NewLimiter(0.5, 5, 20, 40)
	}
	if deps.ContactLimiter == nil {
		// One send per minute per visitor, burst 3; globally 5 per second,
		// burst 20, so a bad loop cannot spam one venue.
		deps.ContactLimiter = resilience.NewLimiter(1/60, 3, 5, 20)
	}
	if deps.PlacesLimiter == nil {
		// A visitor tapping pins makes one details call per tap and one photo
		// call per bubble; 3 per second with a burst of 15 is far above
		// reading speed and well below a scraper's.
		deps.PlacesLimiter = resilience.NewLimiter(3, 15, 40, 80)
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
		loginLimiter: deps.LoginLimiter, regenLimiter: deps.RegenLimiter,
		contactLimiter: deps.ContactLimiter,
		placesLimiter: deps.PlacesLimiter, placesBase: deps.PlacesBaseURL,
		idem: deps.Idem, renderer: deps.Renderer, auth: deps.Auth, probes: deps.Probes, async: deps.Async,
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
