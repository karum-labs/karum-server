package httpapi

import (
	"testing"

	"github.com/karum-labs/karum-server/internal/orchestrator"
)

func TestScoreOfFallsBackWhenStoredSourceIsEmpty(t *testing.T) {
	score := scoreOf(orchestrator.SubScore{Value: 0.5, RawValue: 0.9}, "qloo_fact", false)
	if score.Source != "qloo_fact" {
		t.Fatalf("source = %q, want the fallback", score.Source)
	}
	if score.Raw != 0.9 {
		t.Fatalf("raw = %v, want 0.9", score.Raw)
	}
}

func TestScoreOfKeepsAnExplicitSource(t *testing.T) {
	score := scoreOf(orchestrator.SubScore{Value: 0.5, Source: "model_inference"}, "qloo_fact", false)
	if score.Source != "model_inference" {
		t.Fatalf("source = %q, want model_inference", score.Source)
	}
}

func TestDoorOfMarksARenormalizedCompositeAsPartial(t *testing.T) {
	door := orchestrator.Door{
		Entity: orchestrator.SeedEntity{ID: "door-1", Name: "Tobalá"},
		Affinity: orchestrator.SubScore{
			Name: "A_taste", Value: 1, RawValue: 0.859, Source: "qloo_fact",
		},
		Composite: &orchestrator.SubScore{
			Name:     "S_door",
			Value:    1,
			RawValue: 100,
			Inputs:   []string{"renormalized"},
			Source:   "model_inference",
		},
	}

	body := doorOf(door, orchestrator.Pitch{}, orchestrator.AuditFinding{})
	if body.Scores.Composite == nil {
		t.Fatal("the composite was dropped")
	}
	if !body.Scores.Composite.Partial {
		t.Fatal("a renormalized composite must be marked partial")
	}
}

func TestDoorOfLeavesACompleteCompositeUnmarked(t *testing.T) {
	door := orchestrator.Door{
		Entity: orchestrator.SeedEntity{ID: "door-1", Name: "Tobalá"},
		Affinity: orchestrator.SubScore{
			Name: "A_taste", Value: 0.8, RawValue: 0.9, Source: "qloo_fact",
		},
		Composite: &orchestrator.SubScore{
			Name:     "S_door",
			Value:    0.8,
			RawValue: 80,
			Inputs:   []string{"three terms"},
			Source:   "model_inference",
		},
	}

	body := doorOf(door, orchestrator.Pitch{}, orchestrator.AuditFinding{})
	if body.Scores.Composite == nil {
		t.Fatal("the composite was dropped")
	}
	if body.Scores.Composite.Partial {
		t.Fatal("a complete composite must not be marked partial")
	}
}
