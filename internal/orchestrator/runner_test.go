package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// --- fakes -----------------------------------------------------------------

type fakeTaste struct {
	calls  []ScoutRequest
	result ScoutResult
	err    error
}

func (f *fakeTaste) ScoutDoors(_ context.Context, req ScoutRequest) (ScoutResult, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

type fakeAgent struct {
	auditCalls    []AuditRequest
	draftCalls    []DraftRequest
	baselineCalls []BaselineRequest

	auditResult    AuditResult
	auditErr       error
	draftResult    DraftResult
	draftErr       error
	baselineResult BaselineResult
	baselineErr    error
}

func (f *fakeAgent) AuditMenu(_ context.Context, req AuditRequest) (AuditResult, error) {
	f.auditCalls = append(f.auditCalls, req)
	return f.auditResult, f.auditErr
}

func (f *fakeAgent) DraftDoorPitch(_ context.Context, req DraftRequest) (DraftResult, error) {
	f.draftCalls = append(f.draftCalls, req)
	return f.draftResult, f.draftErr
}

func (f *fakeAgent) LlmOnlyBaseline(_ context.Context, req BaselineRequest) (BaselineResult, error) {
	f.baselineCalls = append(f.baselineCalls, req)
	return f.baselineResult, f.baselineErr
}

// --- fixtures --------------------------------------------------------------

func testDoor(id, name string, rank int) Door {
	return Door{
		Entity: SeedEntity{ID: id, Type: "urn:entity:place", Name: name, City: "Austin"},
		Rank:   rank,
		Affinity: SubScore{
			Name:     "cultural_affinity",
			Value:    1.0,
			RawValue: 0.91,
			Inputs:   []string{"rank=1", "shortlist=25"},
		},
		SharedTags:  []Tag{{URN: "urn:tag:genre:place:restaurant:bar", Label: "Bars"}},
		EvidenceIDs: []string{"ev-0001"},
	}
}

func testRequest(freeEntry bool) Request {
	return Request{
		PresetID:     "austin-wholesale",
		Mode:         ModeWholesale,
		FreeEntry:    freeEntry,
		Seed:         []SeedEntity{{ID: "seed-1", Type: "urn:entity:brand", Name: "Ilegal Mezcal", City: "Austin"}},
		Point:        Point{Lat: 30.2672, Lng: -97.7431},
		RadiusMeters: 8000,
		CategoryTags: []string{"urn:tag:genre:place:restaurant:bar"},
		Brand: BrandContext{
			Name: "Ilegal Mezcal",
			SKUs: []SKU{{ID: "sk-espadin", Name: "Espadín Joven"}},
		},
		City: "Austin",
	}
}

func healthyTaste() *fakeTaste {
	return &fakeTaste{result: ScoutResult{
		Doors: []Door{testDoor("d-1", "Whisler's", 1), testDoor("d-2", "Here Nor There", 2)},
		Skip:  []SkipItem{{Entity: SeedEntity{ID: "d-3", Name: "Maggie Mae's"}, Reason: "low_affinity"}},
		Evidence: []EvidenceCall{
			{ID: "ev-0001", Endpoint: "/v2/insights", ResultCount: 25},
		},
	}}
}

func healthyAgent() *fakeAgent {
	return &fakeAgent{
		baselineResult: BaselineResult{
			Text:       "Wide-band advice with no taste data.",
			VenueNames: []string{"La Condesa", "Suerte"},
		},
		auditResult: AuditResult{Findings: []AuditFinding{
			{Door: SeedEntity{ID: "d-1", Name: "Whisler's"}, Succeeded: true, ShelfOpportunity: 0.75},
			{Door: SeedEntity{ID: "d-2", Name: "Here Nor There"}, Succeeded: true, ShelfUnknown: true},
		}},
		draftResult: DraftResult{
			Script:         "Whisler's ranks first of 25 (ev-0001).",
			RecommendedSKU: SKU{ID: "sk-espadin", Name: "Espadín Joven"},
		},
	}
}

func upstream(message string) error {
	return &UpstreamError{Code: CodeUpstream, Message: message, Cause: errors.New(message)}
}

func TestDraftsReceiveTheRunsEvidenceSetAsTheAllowSet(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, err := runner.RegenerateDoor(context.Background(), run.ID, "d-1", nil, "", "", "", nil); err != nil {
		t.Fatalf("RegenerateDoor: %v", err)
	}
	if len(agent.draftCalls) == 0 {
		t.Fatal("no draft was requested")
	}
	for _, call := range agent.draftCalls {
		if len(call.EvidenceIDs) == 0 {
			t.Fatal("the draft received no allow-set, so every cited id would pass")
		}
		found := false
		for _, id := range call.EvidenceIDs {
			if id == "ev-0001" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the run's evidence set is missing ev-0001: %v", call.EvidenceIDs)
		}
	}
}

func TestAuditTargetsCarryTheQlooNames(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	if _, err := runner.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(agent.auditCalls) == 0 {
		t.Fatal("no audit was requested")
	}
	targets := agent.auditCalls[0].Targets
	if len(targets) == 0 {
		t.Fatal("the audit had no targets")
	}
	names := targets[0].QlooIncumbentNames
	seedNamed := false
	skipNamed := false
	for _, name := range names {
		if name == "Ilegal Mezcal" {
			seedNamed = true
		}
		if name == "Maggie Mae's" {
			skipNamed = true
		}
	}
	if !seedNamed || !skipNamed {
		t.Fatalf("the Qloo names did not reach the audit: %v", names)
	}
}

// --- tests -----------------------------------------------------------------

func TestRunReadyStoresBaselineAndPitches(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.State != StateReady {
		t.Fatalf("state = %q, want ready", run.State)
	}
	if !run.BaselineStored || run.Baseline.Text == "" {
		t.Fatal("baseline was not stored beside the run")
	}
	if len(run.Doors) != 2 || len(run.Skip) != 1 {
		t.Fatalf("doors/skips = %d/%d, want 2/1", len(run.Doors), len(run.Skip))
	}
	// A run never drafts pitches; they are generated per door on demand.
	if len(run.Pitches) != 0 || len(agent.draftCalls) != 0 {
		t.Fatalf("pitches/draft calls = %d/%d, want 0/0", len(run.Pitches), len(agent.draftCalls))
	}
	// The trace covers every hop the client timeline renders.
	stages := map[Stage]bool{}
	for _, event := range run.Trace {
		stages[event.Stage] = true
	}
	for _, want := range []Stage{StageBaseline, StageScout, StageAudit} {
		if !stages[want] {
			t.Errorf("trace missing stage %q", want)
		}
	}
}

func TestRunKeepsDoorsWhenVertexIsDown(t *testing.T) {
	taste := healthyTaste()
	agent := &fakeAgent{
		baselineErr: upstream("Vertex is down"),
		auditErr:    upstream("Vertex is down"),
		draftErr:    upstream("Vertex is down"),
	}
	runner := NewRunner(taste, agent, NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("an AI failure must not fail the run: %v", err)
	}
	if run.State != StateDegraded {
		t.Fatalf("state = %q, want degraded", run.State)
	}
	if len(run.Doors) != 2 || len(run.Skip) != 1 {
		t.Fatalf("taste ranking was dropped: doors=%d skips=%d", len(run.Doors), len(run.Skip))
	}
	if len(run.Pitches) != 0 {
		t.Errorf("a failed draft must leave no pitch, got %d", len(run.Pitches))
	}
	if !run.BaselineStored {
		t.Fatal("a failed baseline must still be recorded beside the run")
	}
	if run.Baseline.ErrorCode != CodeUpstream {
		t.Errorf("baseline code = %q, want upstream", run.Baseline.ErrorCode)
	}
}

func TestFreeEntryFeedsBaselineNamesToTheSkipList(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	if _, err := runner.Run(context.Background(), testRequest(true)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := taste.calls[0].SkipCandidateNames
	want := agent.baselineResult.VenueNames
	if len(got) != len(want) {
		t.Fatalf("skip candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skip candidates = %v, want %v", got, want)
		}
	}
}

func TestPresetDoesNotFeedBaselineNames(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	if _, err := runner.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(taste.calls[0].SkipCandidateNames) != 0 {
		t.Fatalf("preset sent baseline names: %v", taste.calls[0].SkipCandidateNames)
	}
}

func TestAuditIsCappedToTheWorkingShortlist(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	doors := make([]Door, 0, 8)
	for i := 1; i <= 8; i++ {
		doors = append(doors, testDoor(string(rune('a'+i)), "Door", i))
	}
	taste.result.Doors = doors
	runner := NewRunner(taste, agent, NewStore())

	if _, err := runner.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(agent.auditCalls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(agent.auditCalls))
	}
	if got := len(agent.auditCalls[0].Targets); got != workingShortlist {
		t.Fatalf("audit targets = %d, want %d", got, workingShortlist)
	}
}

func TestRegenerateDoorCallsOnlyTheDraft(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	scoutCalls := len(taste.calls)
	auditCalls := len(agent.auditCalls)
	draftCalls := len(agent.draftCalls)

	agent.draftResult.Script = "Regenerated script (ev-0001)."
	updated, err := runner.RegenerateDoor(context.Background(), run.ID, "d-1", nil, "", "", "", nil)
	if err != nil {
		t.Fatalf("RegenerateDoor: %v", err)
	}
	if len(taste.calls) != scoutCalls {
		t.Fatalf("regenerate re-ran taste: %d -> %d calls", scoutCalls, len(taste.calls))
	}
	if len(agent.auditCalls) != auditCalls {
		t.Fatalf("regenerate re-ran the audit: %d -> %d calls", auditCalls, len(agent.auditCalls))
	}
	if len(agent.draftCalls) != draftCalls+1 {
		t.Fatalf("draft calls = %d, want %d", len(agent.draftCalls), draftCalls+1)
	}
	if updated.Pitches["d-1"].Script != agent.draftResult.Script {
		t.Fatalf("script = %q, want %q", updated.Pitches["d-1"].Script, agent.draftResult.Script)
	}
}

func TestRegenerateFailureKeepsThePreviousPitch(t *testing.T) {
	taste, agent := healthyTaste(), healthyAgent()
	runner := NewRunner(taste, agent, NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := run.Pitches["d-1"].Script

	agent.draftErr = upstream("Vertex went away")
	_, err = runner.RegenerateDoor(context.Background(), run.ID, "d-1", nil, "", "", "", nil)
	if err == nil {
		t.Fatal("expected a typed error")
	}
	if _, ok := err.(*UpstreamError); !ok {
		t.Fatalf("error = %T, want *UpstreamError", err)
	}
	if run.Pitches["d-1"].Script != before {
		t.Fatal("a failed regenerate replaced the previous pitch")
	}
}

func TestCodeOfMapsTypedAndUntypedFailures(t *testing.T) {
	if got := CodeOf(&UpstreamError{Code: CodeValidation}); got != CodeValidation {
		t.Fatalf("typed code = %q, want validation", got)
	}
	if got := CodeOf(errors.New("boom")); got != CodeUpstream {
		t.Fatalf("untyped code = %q, want upstream", got)
	}
}

func TestTasteFailureFailsTheRun(t *testing.T) {
	taste := healthyTaste()
	taste.err = &UpstreamError{Code: CodeUpstream, Message: "Qloo down"}
	runner := NewRunner(taste, healthyAgent(), NewStore())

	run, err := runner.Run(context.Background(), testRequest(false))
	if err == nil {
		t.Fatal("expected a taste error")
	}
	if run.State != StateFailed {
		t.Fatalf("state = %q, want failed", run.State)
	}
}

