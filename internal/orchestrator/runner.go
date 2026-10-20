package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/karum-labs/karum-server/internal/places"
	"github.com/karum-labs/karum-server/internal/resilience"
	"github.com/karum-labs/karum-server/internal/routes"
)

// workingShortlist caps the menu audit at the doors that can appear on the
// board, matching service-ai's grounding cap (PLAN.md §16).
const workingShortlist = 5

// ErrRunNotFound is returned when a run id is unknown.
var ErrRunNotFound = errors.New("run not found")

// ErrDoorNotFound is returned when a door id is not in the run.
var ErrDoorNotFound = errors.New("door not found")

// Request is one confirmed scout request. Ids are resolved before this point,
// so a run never pauses for confirmation. RawBrandQuery is set only when the
// seed is not yet confirmed; presets and confirmed seeds leave it empty.
type Request struct {
	PresetID         string
	Mode             Mode
	FreeEntry        bool
	Seed             []SeedEntity
	Point            Point
	RadiusMeters     int
	CategoryTags     []string
	SkipCandidateIDs []string
	Brand            BrandContext
	City             string
	RawBrandQuery    string
	RawFallbacks     []string
	TraceID          string
	CampaignID       string
	HeatmapRadius    int
	PointAttribution string
	FormattedAddress string
	// AccountID is the optional judge account attributed to the run. Empty
	// keeps the anonymous demo path unchanged.
	AccountID string
	// ID is set by the HTTP layer so a job id exists before the pipeline
	// starts and an abort can cancel it. Empty lets the runner allocate one.
	ID string
}

// Runner drives baseline, scout, audit, and drafts for one request.
type Runner struct {
	taste TasteClient
	agent AgentClient
	store RunStore
	now   func() time.Time

	places  places.Enricher
	routes  routes.Planner
	seeds   SeedResolver
	heatmap HeatmapClient
	compare CompareClient

	tasteTimeout  time.Duration
	aiTimeout     time.Duration
	placesTimeout time.Duration
	routesTimeout time.Duration

	tasteBreaker  *resilience.Breaker
	aiBreaker     *resilience.Breaker
	placesBreaker *resilience.Breaker
	routesBreaker *resilience.Breaker
}

// NewRunner builds a runner over the given transports. A nil store is created
// so tests can ignore storage.
func NewRunner(taste TasteClient, agent AgentClient, store RunStore) *Runner {
	if store == nil {
		store = NewStore()
	}
	return &Runner{
		taste:         taste,
		agent:         agent,
		store:         store,
		now:           time.Now,
		tasteBreaker:  resilience.NewBreaker(5, 30*time.Second),
		aiBreaker:     resilience.NewBreaker(5, 30*time.Second),
		placesBreaker: resilience.NewBreaker(5, 30*time.Second),
		routesBreaker: resilience.NewBreaker(5, 30*time.Second),
	}
}

// WithPlaces attaches the Places enricher.
func (r *Runner) WithPlaces(p places.Enricher) *Runner { r.places = p; return r }

// WithRoutes attaches the field-route planner.
func (r *Runner) WithRoutes(p routes.Planner) *Runner { r.routes = p; return r }

// WithSeedResolver attaches raw-query resolution.
func (r *Runner) WithSeedResolver(s SeedResolver) *Runner { r.seeds = s; return r }

// WithHeatmap attaches the flagship heatmap client.
func (r *Runner) WithHeatmap(h HeatmapClient) *Runner { r.heatmap = h; return r }

// WithCompare attaches the conquest compare client.
func (r *Runner) WithCompare(c CompareClient) *Runner { r.compare = c; return r }

// WithDeadlines overrides the per-dependency RPC deadlines. Zero keeps the
// default applied at the call.
func (r *Runner) WithDeadlines(taste, ai, placesDeadline, routesDeadline time.Duration) *Runner {
	r.tasteTimeout = taste
	r.aiTimeout = ai
	r.placesTimeout = placesDeadline
	r.routesTimeout = routesDeadline
	return r
}

// Store exposes the run store for the HTTP layer.
func (r *Runner) Store() RunStore { return r.store }

// Run executes baseline, scouting, auditing, and drafting.
//
// A taste failure returns a failed run and the error. A Vertex failure never
// drops the taste ranking: the run is marked degraded and every door gets a
// template pitch built from evidence fields (PLAN.md §18).
func (r *Runner) Run(ctx context.Context, req Request) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolvedRaw := false
	if len(req.Seed) == 0 && req.RawBrandQuery != "" {
		resolved, err := r.resolveRaw(ctx, req)
		if err != nil {
			return nil, err
		}
		if resolved.Unresolved {
			run := r.blank(req)
			run.State = StateFailed
			run.ErrorCode = firstNonEmpty(resolved.ErrorCode, CodeUnresolved)
			run.ErrorMessage = resolved.ErrorMessage
			r.trace(run, StageSeed, time.Now(), resolved.ErrorMessage, false)
			return run, nil
		}
		req.Seed = resolved.Entities
		resolvedRaw = true
	}
	if len(req.Seed) == 0 {
		return nil, fmt.Errorf("seed is required")
	}
	if req.RadiusMeters <= 0 {
		return nil, fmt.Errorf("radius_meters must be positive")
	}
	if req.Mode == "" {
		req.Mode = ModeWholesale
	}

	run := r.blank(req)
	if resolvedRaw {
		r.trace(run, StageSeed, time.Now(), fmt.Sprintf("%d entities", len(req.Seed)), false)
	}
	degraded := false

	// The baseline runs first so a free-entry scout can use its venue names as
	// skip candidates (PLAN.md §17).
	started := time.Now()
	var baseline BaselineResult
	err := r.callAI(ctx, func(ctx context.Context) error {
		var callErr error
		baseline, callErr = r.agent.LlmOnlyBaseline(ctx, BaselineRequest{
			Brand: req.Brand,
			City:  req.City,
			Mode:  req.Mode,
		})
		return callErr
	})
	if err != nil {
		baseline = BaselineResult{ErrorCode: CodeOf(err), ErrorMessage: err.Error()}
		degraded = true
		run.Warnings = append(run.Warnings, "baseline unavailable: "+err.Error())
	}
	run.Baseline = baseline
	run.BaselineStored = true
	r.trace(run, StageBaseline, started, "LLM-only baseline stored", false)

	if err := r.aborted(ctx, run); err != nil {
		return run, err
	}

	scoutReq := ScoutRequest{
		Seed:             req.Seed,
		Point:            req.Point,
		RadiusMeters:     req.RadiusMeters,
		CategoryTags:     req.CategoryTags,
		SkipCandidateIDs: req.SkipCandidateIDs,
		Mode:             req.Mode,
	}
	if req.FreeEntry {
		scoutReq.SkipCandidateNames = baseline.VenueNames
	}

	scout, err := r.scoutAndHeatmap(ctx, run, req, scoutReq)
	if err != nil {
		return run, err
	}
	run.Doors = scout.Doors
	run.Skip = scout.Skip
	run.Evidence = scout.Evidence
	run.Warnings = append(run.Warnings, scout.Warnings...)
	run.AffinityFloor = scout.AffinityFloor
	if payload, mErr := json.Marshal(scout); mErr == nil {
		run.QlooPayload = payload
	}

	if scout.ErrorCode != "" && scout.ErrorCode != CodeUnspecified {
		run.State = StateFailed
		run.Warnings = append(run.Warnings, scout.ErrorMessage)
		r.store.Put(run)
		return run, nil
	}
	if len(scout.Doors) == 0 {
		run.State = StateFailed
		run.ErrorCode = CodeEmpty
		run.Warnings = append(run.Warnings, "no doors matched the seed")
		r.store.Put(run)
		return run, nil
	}

	r.enrich(ctx, run, req)
	if err := r.aborted(ctx, run); err != nil {
		return run, err
	}

	started = time.Now()
	var audit AuditResult
	var auditErr error
	if len(run.Doors) == 0 {
		r.trace(run, StageAudit, started, "no open doors to audit", false)
	} else {
		auditErr = r.callAI(ctx, func(ctx context.Context) error {
			var callErr error
			audit, callErr = r.agent.AuditMenu(ctx, AuditRequest{
				Mode:    run.Mode,
				Targets: auditTargets(run.Doors, qlooNames(run, req)),
			})
			return callErr
		})
	}
	switch {
	case auditErr != nil:
		degraded = true
		run.Warnings = append(run.Warnings, "menu audit unavailable: "+auditErr.Error())
		r.trace(run, StageAudit, started, auditErr.Error(), false)
	case audit.ErrorCode != "" && audit.ErrorCode != CodeUnspecified:
		degraded = true
		message := audit.ErrorMessage
		if message == "" {
			message = audit.ErrorCode
		}
		run.Warnings = append(run.Warnings, "menu audit unavailable: "+message)
		r.trace(run, StageAudit, started, message, false)
	default:
		for _, finding := range audit.Findings {
			run.Findings[finding.Door.ID] = finding
		}
		if payload, mErr := json.Marshal(audit); mErr == nil {
			run.GroundingPayload = payload
		}
		r.trace(run, StageAudit, started, fmt.Sprintf("%d findings", len(audit.Findings)), false)
	}

	r.compareDoors(ctx, run, req)
	r.scoreDoors(run)
	if err := r.aborted(ctx, run); err != nil {
		return run, err
	}

	for _, door := range run.Doors {
		if err := r.aborted(ctx, run); err != nil {
			return run, err
		}
		r.draftDoor(ctx, run, door, req, &degraded)
	}

	r.routeDoors(ctx, run)

	if degraded {
		run.State = StateDegraded
	} else {
		run.State = StateReady
	}
	r.trace(run, StagePersist, time.Now(), string(run.State), false)
	return run, nil
}

// RegenerateDoor re-drafts one door through the draft RPC only. It never calls
// service-taste, so the Qloo ranking and evidence are not recomputed
// (PLAN.md §18).
func (r *Runner) RegenerateDoor(
	ctx context.Context,
	runID string,
	doorID string,
	evidenceIDs []string,
) (*Run, error) {
	run, ok := r.store.Get(runID)
	if !ok {
		return nil, ErrRunNotFound
	}
	door, ok := findDoor(run.Doors, doorID)
	if !ok {
		return nil, ErrDoorNotFound
	}

	finding, ok := run.Findings[doorID]
	if !ok {
		finding = AuditFinding{Door: door.Entity, ShelfUnknown: true}
	}
	if len(evidenceIDs) == 0 {
		evidenceIDs = runEvidenceIDs(run, door)
	}

	started := time.Now()
	draft, err := r.agent.DraftDoorPitch(ctx, DraftRequest{
		Brand:       run.Brand,
		Door:        door,
		Finding:     finding,
		City:        run.City,
		Mode:        run.Mode,
		EvidenceIDs: evidenceIDs,
	})
	if err != nil {
		r.trace(run, StageRegenerate, started, err.Error(), false)
		return run, err
	}
	if draft.ErrorCode != "" && draft.ErrorCode != CodeUnspecified {
		r.trace(run, StageRegenerate, started, draft.ErrorMessage, false)
		return run, &UpstreamError{Code: draft.ErrorCode, Message: draft.ErrorMessage}
	}
	if draft.Script == "" {
		r.trace(run, StageRegenerate, started, "empty script", false)
		return run, &UpstreamError{Code: CodeValidation, Message: "the draft returned an empty script"}
	}

	run.Pitches[doorID] = Pitch{
		Script:         draft.Script,
		Battlecard:     draft.Battlecard,
		RecommendedSKU: draft.RecommendedSKU,
		ModeParagraph:  draft.ModeParagraph,
		ModelID:        draft.ModelID,
		PromptVersion:  draft.PromptVersion,
		EvidenceIDs:    draft.EvidenceIDs,
	}
	r.trace(run, StageRegenerate, started, draftDetail(door, draft), false)
	r.store.Put(run)
	return run, nil
}

// draftDoor drafts one door, falling back to a template on any Vertex failure.
func (r *Runner) draftDoor(
	ctx context.Context,
	run *Run,
	door Door,
	req Request,
	degraded *bool,
) {
	finding, ok := run.Findings[door.Entity.ID]
	if !ok {
		finding = AuditFinding{Door: door.Entity, ShelfUnknown: true}
	}

	started := time.Now()
	var draft DraftResult
	err := r.callAI(ctx, func(ctx context.Context) error {
		var callErr error
		draft, callErr = r.agent.DraftDoorPitch(ctx, DraftRequest{
			Brand:   req.Brand,
			Door:    door,
			Finding: finding,
			City:    req.City,
			Mode:    run.Mode,
			// The run's evidence set is the allow-set a cited id must belong
			// to; an id outside it is dropped (PLAN.md §17).
			EvidenceIDs: runEvidenceIDs(run, door),
		})
		return callErr
	})
	if err == nil && (draft.ErrorCode == "" || draft.ErrorCode == CodeUnspecified) && draft.Script != "" {
		run.Pitches[door.Entity.ID] = Pitch{
			Script:         draft.Script,
			Battlecard:     draft.Battlecard,
			RecommendedSKU: draft.RecommendedSKU,
			ModeParagraph:  draft.ModeParagraph,
			ModelID:        draft.ModelID,
			PromptVersion:  draft.PromptVersion,
			EvidenceIDs:    draft.EvidenceIDs,
		}
		r.trace(run, StageDraft, started, draftDetail(door, draft), false)
		return
	}

	*degraded = true
	message := "draft unavailable"
	if err != nil {
		message = err.Error()
	} else if draft.ErrorMessage != "" {
		message = draft.ErrorMessage
	}
	run.Warnings = append(run.Warnings, fmt.Sprintf("draft for %s: %s", door.Entity.Name, message))
	run.Pitches[door.Entity.ID] = templatePitch(door, finding, req.Brand, run.Mode)
	r.trace(run, StageDraft, started, "template: "+door.Entity.Name, false)
}

// draftDetail names the door and the model that wrote the pitch, so the
// timeline records which model produced the text (PLAN.md §3).
func draftDetail(door Door, draft DraftResult) string {
	if draft.ModelID == "" {
		return door.Entity.Name
	}
	return door.Entity.Name + " (" + draft.ModelID + ")"
}

// templatePitch builds a usable pitch from evidence fields only, so a degraded
// run never invents a number (PLAN.md §18).
func templatePitch(door Door, finding AuditFinding, brand BrandContext, mode Mode) Pitch {
	evidenceID := ""
	if len(door.EvidenceIDs) > 0 {
		evidenceID = door.EvidenceIDs[0]
	}

	brandName := brand.Name
	if brandName == "" {
		brandName = "the brand"
	}
	parts := []string{fmt.Sprintf("%s is on the returned shortlist for %s.", door.Entity.Name, brandName)}
	if evidenceID != "" {
		parts = append(parts, fmt.Sprintf("Ranking call: %s.", evidenceID))
	}
	if len(door.SharedTags) > 0 {
		labels := make([]string, 0, len(door.SharedTags))
		for _, tag := range door.SharedTags {
			label := tag.Label
			if label == "" {
				label = tag.URN
			}
			labels = append(labels, label)
		}
		parts = append(parts, "Shared taste tags: "+strings.Join(labels, ", ")+".")
	}

	pitch := Pitch{
		Script:         strings.Join(parts, " "),
		RecommendedSKU: firstSKU(brand),
		Template:       true,
	}
	if mode == ModeWholesale {
		pitch.ModeParagraph = ""
	}
	if len(finding.Incumbents) > 0 {
		incumbent := finding.Incumbents[0].Name
		pitch.Battlecard = &Battlecard{
			Headline:      "Displace " + incumbent,
			IncumbentName: incumbent,
			TalkingPoints: []string{
				fmt.Sprintf("%s is already on the shelf.", incumbent),
			},
		}
	}
	return pitch
}

// auditTargets caps the audit at the working shortlist. The Qloo names travel
// with each target so a competitor the model names is checked against the
// payload as well as against the grounding sources (PLAN.md §16).
func auditTargets(doors []Door, qlooNames []string) []AuditTarget {
	limit := workingShortlist
	if len(doors) < limit {
		limit = len(doors)
	}
	targets := make([]AuditTarget, 0, limit)
	for _, door := range doors[:limit] {
		website := ""
		if door.Enrichment != nil && !door.Enrichment.Unknown {
			website = door.Enrichment.Website
		}
		targets = append(targets, AuditTarget{
			Door:               door.Entity,
			Tags:               door.SharedTags,
			Website:            website,
			QlooIncumbentNames: append([]string(nil), qlooNames...),
		})
	}
	return targets
}

// qlooNames are the entity names the Qloo payload carries for this run: the
// resolved seed entities and the resolved skip candidates. A competitor named
// in prose survives only when a grounding source or one of these names
// carries it (PLAN.md §16).
func qlooNames(run *Run, req Request) []string {
	names := make([]string, 0, len(req.Seed)+len(run.Skip))
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	for _, entity := range req.Seed {
		add(entity.Name)
	}
	for _, item := range run.Skip {
		add(item.Entity.Name)
	}
	return names
}

// runEvidenceIDs is the run's evidence set: the ids a cited number may point
// at. A sentence that cites anything outside this set is dropped
// (PLAN.md §17, §18).
func runEvidenceIDs(run *Run, door Door) []string {
	ids := make([]string, 0, len(run.Evidence)+len(door.EvidenceIDs))
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, call := range run.Evidence {
		add(call.ID)
	}
	for _, id := range door.EvidenceIDs {
		add(id)
	}
	return ids
}

func findDoor(doors []Door, id string) (Door, bool) {
	for _, door := range doors {
		if door.Entity.ID == id {
			return door, true
		}
	}
	return Door{}, false
}

func firstSKU(brand BrandContext) SKU {
	if len(brand.SKUs) == 0 {
		return SKU{}
	}
	return brand.SKUs[0]
}

func (r *Runner) trace(run *Run, stage Stage, started time.Time, detail string, cacheHit bool) {
	seq := int64(len(run.Trace) + 1)
	run.Trace = append(run.Trace, TraceEvent{
		Sequence:  seq,
		Cursor:    strconv.Itoa(int(seq)),
		Stage:     stage,
		ElapsedMS: time.Since(started).Milliseconds(),
		CacheHit:  cacheHit,
		Detail:    detail,
	})
	r.store.Put(run)
}
