// Package score assembles the door composite the gateway owns.
//
// service-taste returns cultural affinity and demographic alignment.
// service-ai returns shelf opportunity. This package computes
//
//	S_door = 0.50·A_taste + 0.30·O_shelf + 0.20·D_demo
//
// from the 0–1 sub-scores and the 0–100 display value. When shelf opportunity
// is unknown the composite renormalizes over the terms that exist. The
// saturation skip is not a term in the composite: four or more direct niche
// competitors, or an affinity strictly below the returned shortlist floor,
// removes the door from the board.
package score

import (
	"math"
	"strconv"
)

const (
	// WeightAffinity is the cultural-affinity coefficient.
	WeightAffinity = 0.50
	// WeightShelf is the shelf-opportunity coefficient.
	WeightShelf = 0.30
	// WeightDemo is the demographic-alignment coefficient.
	WeightDemo = 0.20
	// CompetitorSkipAt is the direct-niche-competitor count that skips a door.
	CompetitorSkipAt = 4
)

// Formula is the expression the evidence payload and the formula tooltip cite.
const Formula = "S_door = 0.50·A_taste + 0.30·O_shelf + 0.20·D_demo"

// Term is one 0–1 sub-score. Known is false when the term must be left out
// of the composite rather than treated as zero.
type Term struct {
	Name  string
	Value float64
	Known bool
}

// Composite is the assembled door score.
type Composite struct {
	// Value is S_door on the 0–1 scale.
	Value float64
	// Display is 100·S_door, the number the card shows.
	Display float64
	// Formula is the canonical expression, including a renormalized note.
	Formula string
	// Inputs names the weights that were actually applied.
	Inputs []string
	// Renormalized is true when a term was unknown and the weights were rescaled.
	Renormalized bool
}

// Assemble computes S_door. Unknown terms are omitted and the remaining
// weights are rescaled so they still sum to 1. With no known terms the
// composite is zero and Renormalized is true.
func Assemble(affinity, shelf, demo Term) Composite {
	type weighted struct {
		term   Term
		weight float64
	}
	parts := []weighted{
		{affinity, WeightAffinity},
		{shelf, WeightShelf},
		{demo, WeightDemo},
	}
	var known []weighted
	for _, part := range parts {
		if part.term.Known && !math.IsNaN(part.term.Value) {
			known = append(known, part)
		}
	}
	out := Composite{Formula: Formula}
	if len(known) == 0 {
		out.Renormalized = true
		out.Inputs = []string{"no known sub-scores"}
		return out
	}
	var weightSum float64
	for _, part := range known {
		weightSum += part.weight
	}
	renormalized := len(known) != len(parts)
	var value float64
	for _, part := range known {
		w := part.weight
		if renormalized {
			w /= weightSum
		}
		clamped := clamp01(part.term.Value)
		value += w * clamped
		out.Inputs = append(out.Inputs, formatInput(part.term.Name, w, clamped))
	}
	out.Value = clamp01(value)
	out.Display = 100 * out.Value
	out.Renormalized = renormalized
	if renormalized {
		out.Inputs = append(out.Inputs, "renormalized")
	}
	return out
}

// BelowFloor reports whether raw affinity is strictly under the shortlist
// floor. A zero floor means the caller has not established one. Equality
// stays on the board.
func BelowFloor(raw, floor float64) bool {
	if floor <= 0 || math.IsNaN(raw) || math.IsNaN(floor) {
		return false
	}
	return raw < floor
}

// Saturated reports whether the competitor count skips the door.
func Saturated(competitorCount int) bool {
	return competitorCount >= CompetitorSkipAt
}

// Floor returns the minimum of the returned shortlist. An empty set has no
// floor. The bottom-decile alternative is not used: the minimum is the floor
// the skip rule compares against, and equality keeps the door.
func Floor(raws []float64) float64 {
	floor := math.NaN()
	for _, raw := range raws {
		if math.IsNaN(raw) {
			continue
		}
		if math.IsNaN(floor) || raw < floor {
			floor = raw
		}
	}
	if math.IsNaN(floor) {
		return 0
	}
	return floor
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func formatInput(name string, weight, value float64) string {
	return name + " weight=" + strconv.FormatFloat(weight, 'f', -1, 64) +
		" value=" + strconv.FormatFloat(value, 'f', -1, 64)
}
