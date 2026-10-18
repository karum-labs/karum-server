// Package orchestrator drives a scout run end to end.
//
// The runner calls service-taste for doors and skip items, then service-ai for
// the menu audit, the door pitch, and the LLM-only baseline. Each completion
// writes a trace event. When Vertex fails, the run is marked degraded and the
// taste ranking stays available with template pitches built from evidence only
// (PLAN.md §15, §17, §18).
//
// The taste and agent transports are interfaces: service clients land with the
// HTTP surface in §19, while these tests use in-process fakes (§40).
package orchestrator

// JobState is the lifecycle state of a scout run. It mirrors the shared proto
// job-state enum so the client maps it one to one.
type JobState string

const (
	// StateResolving means entity resolution is in flight.
	StateResolving JobState = "resolving"
	// StateRunning means the main pipeline is executing.
	StateRunning JobState = "running"
	// StateReady means the campaign is available.
	StateReady JobState = "ready"
	// StateDegraded means the run finished without Vertex output.
	StateDegraded JobState = "degraded"
	// StateFailed means the run could not produce a door list.
	StateFailed JobState = "failed"
)

// Stage names a step in the run lifecycle. They label trace events so the
// client timeline can show what finished.
type Stage string

const (
	// StageBaseline calls the LLM-only counterfactual.
	StageBaseline Stage = "baseline"
	// StageScout calls service-taste for doors and skip items.
	StageScout Stage = "scout"
	// StageAudit calls service-ai for the menu audit.
	StageAudit Stage = "menu_grounding"
	// StageDraft calls service-ai for one door pitch.
	StageDraft Stage = "draft"
	// StageRegenerate calls only the draft RPC for one door.
	StageRegenerate Stage = "regenerate"
	// StageSeed resolves a raw brand query. Confirmed seeds skip it.
	StageSeed Stage = "seed_resolution"
	// StageHeatmap fetches the flagship density layer.
	StageHeatmap Stage = "heatmap"
	// StagePlaces enriches the working set.
	StagePlaces Stage = "places"
	// StageCompare runs the conquest comparison for an incumbent door.
	StageCompare Stage = "compare"
	// StageScore assembles S_door and applies the saturation rule.
	StageScore Stage = "scoring"
	// StageRoute orders the qualified doors.
	StageRoute Stage = "route"
	// StagePersist freezes the evidence snapshot.
	StagePersist Stage = "persist"
)
