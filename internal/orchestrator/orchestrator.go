// Package orchestrator drives an analysis job end to end.
//
// This is a skeleton. The job runner will:
//
//  1. Geocode the vacancy address and return candidate points when ambiguous.
//  2. Call service-taste over gRPC to resolve neighbour entities, pausing in
//     "awaiting_confirmation" when a match is low confidence.
//  3. Fetch the block profile, score candidates, and run the AI probe.
//  4. Enrich pins via Places and optionally compute a site-visit route.
//
// Each step completion writes a trace event that the SSE stream emits. See
// PLAN.md epic 5 (Gateway) for the full contract.
package orchestrator

import (
	"context"
	"fmt"

	"github.com/karum-labs/karum-server/internal/config"
)

// Stage names a step in the job lifecycle. They are emitted in trace events so
// the client timeline can label each row.
type Stage string

const (
	// StageGeocode resolves the vacancy address to a point.
	StageGeocode Stage = "geocode"
	// StageResolve calls service-taste to resolve neighbour entities.
	StageResolve Stage = "resolve"
	// StageProfile fetches the block profile from service-taste.
	StageProfile Stage = "profile"
	// StageScore ranks candidates in service-taste.
	StageScore Stage = "score"
	// StageProbe runs the AI contradiction probe.
	StageProbe Stage = "probe"
	// StageEnrich enriches pins via the Places API.
	StageEnrich Stage = "enrich"
)

// JobState is the lifecycle state of an analysis job. It mirrors the shared
// proto job-state enum so the client maps it one to one.
type JobState string

const (
	// StateResolving means entity resolution is in flight.
	StateResolving JobState = "resolving"
	// StateAwaitingConfirmation means the job is paused for user input.
	StateAwaitingConfirmation JobState = "awaiting_confirmation"
	// StateRunning means the main pipeline is executing.
	StateRunning JobState = "running"
	// StateReady means the shortlist is available.
	StateReady JobState = "ready"
	// StateDegraded means the run finished on the deterministic autopilot.
	StateDegraded JobState = "degraded"
	// StateFailed means the run could not produce a shortlist.
	StateFailed JobState = "failed"
)

// Orchestrator runs analysis jobs against the two microservices.
type Orchestrator struct {
	cfg *config.Config
}

// New builds an Orchestrator bound to the given config.
func New(cfg *config.Config) *Orchestrator {
	return &Orchestrator{cfg: cfg}
}

// Run executes the full pipeline for a single vacancy. It is a stub: the real
// implementation dials service-taste and service-ai over gRPC and persists run
// state in Postgres.
//
// TODO(orchestration): implement resolve -> confirm -> profile -> score ->
// probe -> baseline, persisting the exact Qloo payloads on the run so a later
// cache refresh cannot rewrite history.
func (o *Orchestrator) Run(ctx context.Context, vacancyID string) (JobState, error) {
	if vacancyID == "" {
		return StateFailed, fmt.Errorf("vacancy id is required")
	}
	return StateFailed, fmt.Errorf("orchestrator not implemented: run %q", vacancyID)
}
