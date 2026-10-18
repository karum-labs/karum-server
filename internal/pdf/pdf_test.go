package pdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSheetSeparatesFactsAndInferences(t *testing.T) {
	raw, err := RunSheet(Document{
		Brand:   "Ilegal Mezcal",
		City:    "Austin",
		Mode:    "wholesale",
		Formula: "S_door = 0.50 A_taste + 0.30 O_shelf + 0.20 D_demo",
		Stops: []Stop{{
			Rank: 1,
			Name: "Whisler's",
			Scores: []Fact{
				{Label: "Cultural affinity", Value: "91 raw", Source: "qloo_fact"},
				{Label: "Shelf opportunity", Value: "75", Source: "model_inference"},
			},
			SKU:   "Espadin",
			Pitch: "Walk in with the espadin.",
			Cites: []string{"ev-0001"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("%PDF")) {
		t.Fatal("output is not a PDF")
	}
	text := extractVisible(raw)
	for _, want := range []string{"Qloo fact", "Model inference", "Whisler's", "ev-0001", "Espadin"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pdf text missing %q\n%s", want, text)
		}
	}
}

func TestSiteBriefNamesCorridors(t *testing.T) {
	raw, err := SiteBrief(Document{
		Brand: "Muji",
		City:  "Brooklyn",
		Mode:  "flagship",
		Corridors: []Corridor{{
			Name:    "Williamsburg",
			Anchors: []string{"Choplet", "Kettl"},
			Demo:    "alignment 0.62",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := extractVisible(raw)
	if !strings.Contains(text, "Williamsburg") || !strings.Contains(text, "Choplet") {
		t.Fatalf("brief text = %s", text)
	}
	if !strings.Contains(text, "Model inference") || !strings.Contains(text, "Qloo fact") {
		t.Fatalf("brief does not separate provenance: %s", text)
	}
}

func extractVisible(raw []byte) string {
	var b strings.Builder
	for _, r := range string(raw) {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}
