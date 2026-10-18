package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// workingShortlist caps the menu audit at the doors that can appear on the
// board, matching service-ai's grounding cap (PLAN.md §16).
const workingShortlist = 5

// ErrRunNotFound is returned when a run id is unknown.
var ErrRunNotFound = errors.New("run not found")

// ErrDoorNotFound is returned when a door id is not in the run.
var ErrDoorNotFound = errors.New("door not found")

// Request is one confirmed scout request. Ids are resolved before this point,
// so a run never pauses for confirmation.
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
}

// Runner drives baseline, scout, audit, and drafts for one request.
type Runner struct {
	taste TasteClient
	agent AgentClient
	store *Store
	now   func() time.Time
}

// NewRunner builds a runner over the given transports. A nil store is created
// so tests can ignore storage.
func NewRunner(taste TasteClient, agent AgentClient, store *Store) *Runner {
	if store == nil {
		store = NewStore()
	}
	return &Runner{taste: taste, agent: agent, store: store, now: time.Now}
}

// Store exposes the run store for the HTTP layer.
func (r *Runner) Store() *Store { return r.store }

// Run executes baseline, scouting, auditing, and drafting.
//
// A taste failure returns a failed run and the error. A Vertex failure never
// drops the taste ranking: the run is marked degraded and every door gets a
// template pitch built from evidence fields (PLAN.md §18).
func (r *Runner) Run(ctx context.Context, req Request) (*Run, error) {
	if len(req.Seed) == 0 {
		return nil, fmt.Errorf("seed is required")
	}
	if req.RadiusMeters <= 0 {
		return nil, fmt.Errorf("radius_meters must be positive")
	}
	if req.Mode == "" {
		req.Mode = ModeWholesale
	}

	run := &Run{
		ID:        r.store.NextID(),
		PresetID:  req.PresetID,
		Mode:      req.Mode,
		FreeEntry: req.FreeEntry,
		Brand:     req.Brand,
		City:      req.City,
		State:     StateRunning,
		Findings:  make(map[string]AuditFinding),
		Pitches:   make(map[string]Pitch),
		CreatedAt: r.now(),
	}
	degraded := false

	// The baseline runs first so a free-entry scout can use its venue names as
	// skip candidates (PLAN.md §17).
	started := time.Now()
	baseline, err := r.agent.LlmOnlyBaseline(ctx, BaselineRequest{
		Brand: req.Brand,
		City:  req.City,
		Mode:  req.Mode,
	})
	if err != nil {
		baseline = BaselineResult{ErrorCode: CodeOf(err), ErrorMessage: err.Error()}
		degraded = true
		run.Warnings = append(run.Warnings, "baseline unavailable: "+err.Error())
	}
	run.Baseline = baseline
	run.BaselineStored = true
	r.trace(run, StageBaseline, started, "LLM-only baseline stored", false)

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

	started = time.Now()
	scout, err := r.taste.ScoutDoors(ctx, scoutReq)
	if err != nil {
		run.State = StateFailed
		run.Warnings = append(run.Warnings, "scout failed: "+err.Error())
		r.trace(run, StageScout, started, err.Error(), false)
		r.store.Put(run)
		return run, err
	}
	run.Doors = scout.Doors
	run.Skip = scout.Skip
	run.Evidence = scout.Evidence
	run.Warnings = append(run.Warnings, scout.Warnings...)
	r.trace(run, StageScout, started, fmt.Sprintf("%d doors, %d skips", len(scout.Doors), len(scout.Skip)), false)

	if scout.ErrorCode != "" && scout.ErrorCode != CodeUnspecified {
		run.State = StateFailed
		run.Warnings = append(run.Warnings, scout.ErrorMessage)
		r.store.Put(run)
		return run, nil
	}
	if len(scout.Doors) == 0 {
		run.State = StateFailed
		run.Warnings = append(run.Warnings, "no doors matched the seed")
		r.store.Put(run)
		return run, nil
	}

	started = time.Now()
	audit, auditErr := r.agent.AuditMenu(ctx, AuditRequest{
		Mode:    run.Mode,
		Targets: auditTargets(run.Doors),
	})
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
		r.trace(run, StageAudit, started, fmt.Sprintf("%d findings", len(audit.Findings)), false)
	}

	for _, door := range run.Doors {
		r.draftDoor(ctx, run, door, req, &degraded)
	}

	if degraded {
		run.State = StateDegraded
	} else {
		run.State = StateReady
	}
	r.store.Put(run)
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
		evidenceIDs = door.EvidenceIDs
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
	}
	r.trace(run, StageRegenerate, started, door.Entity.Name, false)
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
	draft, err := r.agent.DraftDoorPitch(ctx, DraftRequest{
		Brand:       req.Brand,
		Door:        door,
		Finding:     finding,
		City:        req.City,
		Mode:        run.Mode,
		EvidenceIDs: door.EvidenceIDs,
	})
	if err == nil && (draft.ErrorCode == "" || draft.ErrorCode == CodeUnspecified) && draft.Script != "" {
		run.Pitches[door.Entity.ID] = Pitch{
			Script:         draft.Script,
			Battlecard:     draft.Battlecard,
			RecommendedSKU: draft.RecommendedSKU,
			ModeParagraph:  draft.ModeParagraph,
		}
		r.trace(run, StageDraft, started, door.Entity.Name, false)
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

// auditTargets caps the audit at the working shortlist.
func auditTargets(doors []Door) []AuditTarget {
	limit := workingShortlist
	if len(doors) < limit {
		limit = len(doors)
	}
	targets := make([]AuditTarget, 0, limit)
	for _, door := range doors[:limit] {
		targets = append(targets, AuditTarget{Door: door.Entity, Tags: door.SharedTags})
	}
	return targets
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
	run.Trace = append(run.Trace, TraceEvent{
		Stage:     stage,
		ElapsedMS: time.Since(started).Milliseconds(),
		CacheHit:  cacheHit,
		Detail:    detail,
	})
}
