package score

import "testing"

func TestAssembleUsesAllThreeWeights(t *testing.T) {
	got := Assemble(
		Term{Name: "A_taste", Value: 1, Known: true},
		Term{Name: "O_shelf", Value: 0.5, Known: true},
		Term{Name: "D_demo", Value: 0, Known: true},
	)
	// 0.50*1 + 0.30*0.5 + 0.20*0 = 0.65
	if !close(got.Value, 0.65) {
		t.Fatalf("value = %v, want 0.65", got.Value)
	}
	if !close(got.Display, 65) {
		t.Fatalf("display = %v, want 65", got.Display)
	}
	if got.Renormalized {
		t.Fatal("a complete set of terms must not renormalize")
	}
	if got.Formula != Formula {
		t.Fatalf("formula = %q", got.Formula)
	}
}

func TestAssembleRenormalizesWhenShelfIsUnknown(t *testing.T) {
	got := Assemble(
		Term{Name: "A_taste", Value: 1, Known: true},
		Term{Name: "O_shelf", Known: false},
		Term{Name: "D_demo", Value: 0.5, Known: true},
	)
	// (0.50*1 + 0.20*0.5) / 0.70 = 0.6/0.7
	if !close(got.Value, 0.6/0.7) {
		t.Fatalf("value = %v, want %v", got.Value, 0.6/0.7)
	}
	if !got.Renormalized {
		t.Fatal("unknown shelf must renormalize over affinity and demographics")
	}
}

func TestAssembleAffinityOnly(t *testing.T) {
	got := Assemble(
		Term{Name: "A_taste", Value: 0.25, Known: true},
		Term{Known: false},
		Term{Known: false},
	)
	if !close(got.Value, 0.25) {
		t.Fatalf("value = %v, want 0.25", got.Value)
	}
}

func TestFloorAndSaturation(t *testing.T) {
	floor := Floor([]float64{0.9, 0.84, 0.91})
	if !close(floor, 0.84) {
		t.Fatalf("floor = %v, want 0.84", floor)
	}
	if BelowFloor(0.84, floor) {
		t.Fatal("equality must stay on the board")
	}
	if !BelowFloor(0.839, floor) {
		t.Fatal("a raw value under the floor must skip")
	}
	if BelowFloor(0.5, 0) {
		t.Fatal("a zero floor is not established")
	}
	if Saturated(3) {
		t.Fatal("three competitors do not skip")
	}
	if !Saturated(4) {
		t.Fatal("four competitors skip the door")
	}
}

func TestEmptyFloor(t *testing.T) {
	if Floor(nil) != 0 {
		t.Fatal("an empty shortlist has no floor")
	}
}

func close(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
