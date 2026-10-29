package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/karum-labs/karum-server/internal/resilience"
	"github.com/karum-labs/karum-server/internal/routes"
	"github.com/karum-labs/karum-server/internal/score"
)

func (r *Runner) blank(req Request) *Run {
	id := req.ID
	if id == "" {
		id = r.store.NextID()
	}
	return &Run{
		ID:               id,
		PresetID:         req.PresetID,
		Mode:             req.Mode,
		FreeEntry:        req.FreeEntry,
		Brand:            req.Brand,
		City:             req.City,
		Seed:             append([]SeedEntity(nil), req.Seed...),
		State:            StateRunning,
		Findings:         map[string]AuditFinding{},
		Pitches:          map[string]Pitch{},
		CreatedAt:        r.now(),
		CampaignID:       req.CampaignID,
		Point:            req.Point,
		RadiusMeters:     req.RadiusMeters,
		TraceID:          req.TraceID,
		PointAttribution: req.PointAttribution,
		FormattedAddress: req.FormattedAddress,
		AccountID:        req.AccountID,
	}
}

func (r *Runner) aborted(ctx context.Context, run *Run) error {
	if err := ctx.Err(); err != nil {
		run.State = StateFailed
		// A cancellation is not an upstream failure, so the run carries a
		// message and no error code.
		run.ErrorMessage = "the run was canceled before it finished"
		run.Warnings = append(run.Warnings, "canceled")
		r.store.Put(run)
		return err
	}
	return nil
}

func (r *Runner) resolveRaw(ctx context.Context, req Request) (SeedResolution, error) {
	if r.seeds == nil {
		return SeedResolution{}, fmt.Errorf("seed is required")
	}
	var resolved SeedResolution
	err := r.callTaste(ctx, func(ctx context.Context) error {
		var callErr error
		resolved, callErr = r.seeds.ResolveBrandSeed(ctx, SeedQuery{
			BrandQuery: req.RawBrandQuery,
			Fallbacks:  req.RawFallbacks,
			Point:      req.Point,
			City:       req.City,
		})
		return callErr
	})
	if err != nil {
		return SeedResolution{}, err
	}
	return resolved, nil
}

func (r *Runner) callTaste(ctx context.Context, fn func(context.Context) error) error {
	if !r.tasteBreaker.Allow() {
		return &UpstreamError{Code: CodeUpstream, Message: "taste circuit open"}
	}
	err := resilience.DoWithRetry(ctx, r.tasteTimeout, 8*time.Second, fn)
	record(r.tasteBreaker, ctx, err)
	return err
}

func (r *Runner) callAI(ctx context.Context, fn func(context.Context) error) error {
	if r.aiBreaker != nil && !r.aiBreaker.Allow() {
		return &UpstreamError{Code: CodeUpstream, Message: "ai circuit open"}
	}
	err := resilience.DoWithRetry(ctx, r.aiTimeout, 30*time.Second, fn)
	record(r.aiBreaker, ctx, err)
	return err
}

// record counts a call outcome on a breaker. A cancelled call, and a call
// the caller's own deadline ended, are not upstream failures: the streak
// stays as it is, so an aborted run never opens the circuit. A deadline the
// gateway set is an upstream failure: the upstream hung.
func record(breaker *resilience.Breaker, ctx context.Context, err error) {
	if breaker == nil {
		return
	}
	switch {
	case err == nil:
		breaker.Record(nil)
	case ctx.Err() != nil:
		// The caller cancelled the run or its own deadline passed.
	case errors.Is(err, context.DeadlineExceeded):
		breaker.Record(&UpstreamError{Code: CodeUpstream, Message: "the upstream call timed out"})
	default:
		breaker.Record(err)
	}
}

// failureOf maps a failed call onto the typed code and the message the
// client shows. A cancelled call is not an upstream failure, so it carries
// no code.
func failureOf(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	if resilience.IsCanceled(err) {
		return "", "the run was canceled before it finished"
	}
	code := CodeOf(err)
	message := err.Error()
	if message == "" {
		message = code
	}
	return code, message
}

func (r *Runner) scoutAndHeatmap(ctx context.Context, run *Run, req Request, scoutReq ScoutRequest) (ScoutResult, error) {
	var (
		scout    ScoutResult
		scoutErr error
		view     HeatmapView
		wg       sync.WaitGroup
	)
	started := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		scoutErr = r.callTaste(ctx, func(ctx context.Context) error {
			var err error
			scout, err = r.taste.ScoutDoors(ctx, scoutReq)
			return err
		})
	}()
	heatStarted := time.Now()
	if req.Mode == ModeFlagship {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view = r.fetchHeatmap(ctx, req)
		}()
	}
	wg.Wait()
	if scoutErr != nil {
		run.State = StateFailed
		run.ErrorCode, run.ErrorMessage = failureOf(scoutErr)
		run.Warnings = append(run.Warnings, "scout failed: "+scoutErr.Error())
		r.trace(run, StageScout, started, scoutErr.Error(), false)
		return ScoutResult{}, scoutErr
	}
	r.trace(run, StageScout, started, fmt.Sprintf("%d doors, %d skips", len(scout.Doors), len(scout.Skip)), false)
	if req.Mode == ModeFlagship {
		run.Heatmap = &view
		detail := fmt.Sprintf("%d cells", view.CellCount)
		if !view.Available {
			detail = view.Reason
			if detail == "" {
				detail = "heatmap unavailable"
			}
			run.Warnings = append(run.Warnings, "heatmap unavailable: "+detail)
		}
		r.trace(run, StageHeatmap, heatStarted, detail, false)
	}
	return scout, nil
}

func (r *Runner) fetchHeatmap(ctx context.Context, req Request) HeatmapView {
	if r.heatmap == nil {
		return HeatmapView{Available: false, Reason: "heatmap client is not configured"}
	}
	radius := req.HeatmapRadius
	if radius <= 0 {
		radius = 10000
	}
	var view HeatmapView
	err := r.callTaste(ctx, func(ctx context.Context) error {
		var callErr error
		view, callErr = r.heatmap.GetLocalityHeatmap(ctx, HeatmapQuery{
			Seed:         req.Seed,
			Point:        req.Point,
			RadiusMeters: radius,
		})
		return callErr
	})
	if err != nil {
		return HeatmapView{Available: false, Reason: err.Error()}
	}
	return view
}

func (r *Runner) enrich(ctx context.Context, run *Run, req Request) {
	started := time.Now()
	if r.places == nil {
		for i := range run.Doors {
			run.Doors[i].Enrichment = &Enrichment{OpenStatus: "unknown", Unknown: true}
		}
		r.trace(run, StagePlaces, started, "places key is not configured", false)
		return
	}
	targets := make([]placesTarget, len(run.Doors))
	for i, door := range run.Doors {
		targets[i] = placesTarget{ID: door.Entity.ID, Name: door.Entity.Name, City: req.City, Lat: req.Point.Lat, Lng: req.Point.Lng}
	}
	var enriched []Enrichment
	err := r.callPlaces(ctx, func(ctx context.Context) error {
		rows, callErr := r.places.Enrich(ctx, toPlaceTargets(targets))
		if callErr != nil && len(rows) == 0 {
			return callErr
		}
		enriched = fromPlaceRows(rows)
		if callErr != nil {
			run.Warnings = append(run.Warnings, "places unavailable: "+callErr.Error())
		}
		return nil
	})
	if err != nil {
		for i := range run.Doors {
			run.Doors[i].Enrichment = &Enrichment{OpenStatus: "unknown", Unknown: true}
		}
		run.Warnings = append(run.Warnings, "places unavailable: "+err.Error())
		r.trace(run, StagePlaces, started, err.Error(), false)
		return
	}
	byID := map[string]Enrichment{}
	for _, row := range enriched {
		byID[row.ID] = row
	}
	kept := make([]Door, 0, len(run.Doors))
	for _, door := range run.Doors {
		row, ok := byID[door.Entity.ID]
		if !ok {
			row = Enrichment{OpenStatus: "unknown", Unknown: true}
		}
		door.Enrichment = &row
		if row.Lat != 0 || row.Lng != 0 {
			door.Lat = row.Lat
			door.Lng = row.Lng
		}
		if row.Closed {
			run.Skip = append(run.Skip, SkipItem{
				Entity:       door.Entity,
				Reason:       "closed",
				ReasonDetail: "the venue is closed per the Places read",
				RawAffinity:  door.Affinity.RawValue,
				Floor:        run.AffinityFloor,
			})
			continue
		}
		kept = append(kept, door)
	}
	run.Doors = kept
	r.trace(run, StagePlaces, started, fmt.Sprintf("%d open", len(kept)), false)
}

type placesTarget struct {
	ID, Name, City string
	Lat, Lng       float64
}

func (r *Runner) callPlaces(ctx context.Context, fn func(context.Context) error) error {
	if r.placesBreaker != nil && !r.placesBreaker.Allow() {
		return &UpstreamError{Code: CodeUpstream, Message: "places circuit open"}
	}
	err := resilience.DoWithRetry(ctx, r.placesTimeout, 5*time.Second, fn)
	record(r.placesBreaker, ctx, err)
	return err
}

func (r *Runner) compareDoors(ctx context.Context, run *Run, req Request) {
	started := time.Now()
	if r.compare == nil {
		return
	}
	brandID := ""
	for _, entity := range req.Seed {
		if entity.Type == "urn:entity:brand" || brandID == "" {
			brandID = entity.ID
			if entity.Type == "urn:entity:brand" {
				break
			}
		}
	}
	compared := 0
	for i, door := range run.Doors {
		finding := run.Findings[door.Entity.ID]
		if len(finding.Incumbents) == 0 {
			continue
		}
		compared++
		var chart CompareChart
		err := r.callTaste(ctx, func(ctx context.Context) error {
			var callErr error
			chart, callErr = r.compare.CompareAudiences(ctx, CompareQuery{
				DoorID:        door.Entity.ID,
				BrandID:       brandID,
				IncumbentName: finding.Incumbents[0].Name,
			})
			return callErr
		})
		if err != nil {
			chart = CompareChart{Available: false, Reason: err.Error()}
			run.Warnings = append(run.Warnings, "compare unavailable for "+door.Entity.Name)
		}
		run.Doors[i].Compare = &chart
	}
	if compared == 0 {
		return
	}
	r.trace(run, StageCompare, started, fmt.Sprintf("%d incumbent doors", compared), false)
}

func (r *Runner) scoreDoors(run *Run) {
	started := time.Now()
	raws := make([]float64, 0, len(run.Doors))
	for _, door := range run.Doors {
		if door.Affinity.RawValue > 0 {
			raws = append(raws, door.Affinity.RawValue)
		}
	}
	floor := run.AffinityFloor
	if floor == 0 {
		floor = score.Floor(raws)
	}
	run.AffinityFloor = floor
	kept := make([]Door, 0, len(run.Doors))
	for _, door := range run.Doors {
		finding := run.Findings[door.Entity.ID]
		switch {
		case score.Saturated(finding.CompetitorCount):
			run.Skip = append(run.Skip, SkipItem{
				Entity:          door.Entity,
				Reason:          "competitor_saturation",
				ReasonDetail:    fmt.Sprintf("%d direct niche competitors on the shelf", finding.CompetitorCount),
				RawAffinity:     door.Affinity.RawValue,
				Floor:           floor,
				CompetitorCount: finding.CompetitorCount,
			})
			continue
		case score.BelowFloor(door.Affinity.RawValue, floor):
			run.Skip = append(run.Skip, SkipItem{
				Entity:       door.Entity,
				Reason:       "low_affinity",
				ReasonDetail: fmt.Sprintf("%.4f below the returned floor %.4f", door.Affinity.RawValue, floor),
				RawAffinity:  door.Affinity.RawValue,
				Floor:        floor,
			})
			continue
		}
		shelf := score.Term{Name: "O_shelf"}
		if finding.Succeeded && !finding.ShelfUnknown {
			shelf.Known = true
			shelf.Value = finding.ShelfOpportunity
			door.ShelfOpportunity = &SubScore{
				Name:     "O_shelf",
				Value:    finding.ShelfOpportunity,
				RawValue: finding.ShelfOpportunity,
				Inputs:   []string{fmt.Sprintf("competitors=%d", finding.CompetitorCount)},
				Source:   "model_inference",
			}
		}
		demo := score.Term{Name: "D_demo"}
		if door.DemographicAlignment != nil {
			demo.Known = true
			demo.Value = door.DemographicAlignment.Value
		}
		assembled := score.Assemble(
			score.Term{Name: "A_taste", Value: door.Affinity.Value, Known: true},
			shelf,
			demo,
		)
		door.Composite = &SubScore{
			Name:     "S_door",
			Value:    assembled.Value,
			RawValue: assembled.Display,
			Inputs:   append([]string{assembled.Formula}, assembled.Inputs...),
			Source:   "model_inference",
		}
		kept = append(kept, door)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		si, sj := 0.0, 0.0
		if kept[i].Composite != nil {
			si = kept[i].Composite.Value
		}
		if kept[j].Composite != nil {
			sj = kept[j].Composite.Value
		}
		if si != sj {
			return si > sj
		}
		return kept[i].Entity.ID < kept[j].Entity.ID
	})
	for i := range kept {
		kept[i].Rank = i + 1
	}
	run.Doors = kept
	run.Formula = score.Formula
	r.trace(run, StageScore, started, fmt.Sprintf("%d doors frozen", len(kept)), false)
}

func (r *Runner) routeDoors(ctx context.Context, run *Run) {
	started := time.Now()
	if r.routes == nil {
		run.Route = &RouteView{Skipped: true, SkipReason: "routes key is not configured"}
		r.trace(run, StageRoute, started, run.Route.SkipReason, false)
		return
	}
	stops := make([]routes.Stop, 0, len(run.Doors))
	for _, door := range run.Doors {
		stops = append(stops, routes.Stop{ID: door.Entity.ID, Name: door.Entity.Name, Lat: door.Lat, Lng: door.Lng})
	}
	if r.routesBreaker != nil && !r.routesBreaker.Allow() {
		run.Route = &RouteView{Failed: true, Error: "routes circuit open", Attribution: routes.Attribution}
		run.Warnings = append(run.Warnings, "route unavailable: routes circuit open")
		r.trace(run, StageRoute, started, "routes circuit open", false)
		return
	}
	var plan routes.Plan
	err := resilience.DoWithRetry(ctx, r.routesTimeout, 5*time.Second, func(ctx context.Context) error {
		var callErr error
		plan, callErr = r.routes.Plan(ctx, string(run.Mode), stops)
		return callErr
	})
	record(r.routesBreaker, ctx, err)
	if err != nil {
		run.Route = &RouteView{Failed: true, Error: err.Error(), Attribution: routes.Attribution}
		run.Warnings = append(run.Warnings, "route unavailable: "+err.Error())
		r.trace(run, StageRoute, started, err.Error(), false)
		return
	}
	run.Route = &RouteView{
		Skipped:     plan.Skipped,
		SkipReason:  plan.SkipReason,
		Failed:      plan.Failed,
		Error:       plan.Error,
		Polyline:    plan.Polyline,
		StopIDs:     plan.StopIDs,
		Attribution: plan.Attribution,
	}
	for _, leg := range plan.Legs {
		run.Route.Legs = append(run.Route.Legs, RouteLeg{
			FromID: leg.FromID, ToID: leg.ToID, Duration: leg.Duration,
			Polyline: leg.Polyline, DistanceMeters: leg.DistanceMeters,
		})
	}
	if plan.Failed {
		run.Warnings = append(run.Warnings, "route unavailable: "+plan.Error)
	}
	detail := plan.SkipReason
	if detail == "" && plan.Failed {
		detail = plan.Error
	}
	if detail == "" {
		detail = fmt.Sprintf("%d stops", len(plan.StopIDs))
	}
	r.trace(run, StageRoute, started, detail, false)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
