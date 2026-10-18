// Package pdf renders the field run-sheet and the flagship site brief.
//
// Qloo facts and model inferences are drawn in distinct styles, matching the
// provenance split in the proto comments. A render error is returned to the
// caller; the on-screen campaign is left untouched.
package pdf

import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

// Fact is one line whose provenance the PDF must show.
type Fact struct {
	Label  string
	Value  string
	Source string // "qloo_fact", "google_fact", or "model_inference"
}

// Stop is one ordered door on the run-sheet.
type Stop struct {
	Rank   int
	Name   string
	Scores []Fact
	SKU    string
	Pitch  string
	Cites  []string
}

// Corridor is one flagship corridor.
type Corridor struct {
	Name    string
	Anchors []string
	Demo    string
}

// Document is the campaign slice either renderer needs.
type Document struct {
	Title     string
	City      string
	Brand     string
	Mode      string
	Formula   string
	Stops     []Stop
	Corridors []Corridor
}

// RunSheet renders the wholesale or collab field sheet.
func RunSheet(doc Document) ([]byte, error) {
	return render(doc, false)
}

// SiteBrief renders the flagship corridor brief.
func SiteBrief(doc Document) ([]byte, error) {
	return render(doc, true)
}

func render(doc Document, brief bool) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(false)
	pdf.SetTitle(doc.Title, false)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 16)
	title := doc.Title
	if title == "" {
		if brief {
			title = "Site-selection brief"
		} else {
			title = "Field run-sheet"
		}
	}
	pdf.CellFormat(0, 10, sanitize(title), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	pdf.MultiCell(0, 5, sanitize(fmt.Sprintf("%s · %s · %s", doc.Brand, doc.City, doc.Mode)), "", "L", false)
	if doc.Formula != "" {
		writeLine(pdf, Fact{Label: "Door score", Value: doc.Formula, Source: "model_inference"})
	}
	pdf.Ln(2)
	writeLegend(pdf)
	if brief {
		if len(doc.Corridors) == 0 {
			pdf.MultiCell(0, 5, "No corridors were returned for this run.", "", "L", false)
		}
		for _, corridor := range doc.Corridors {
			pdf.Ln(2)
			pdf.SetFont("Helvetica", "B", 13)
			pdf.CellFormat(0, 8, sanitize(corridor.Name), "", 1, "L", false, 0, "")
			writeLine(pdf, Fact{Label: "Corridor", Value: corridor.Name, Source: "qloo_fact"})
			if len(corridor.Anchors) > 0 {
				writeLine(pdf, Fact{Label: "Anchor neighbours", Value: join(corridor.Anchors), Source: "qloo_fact"})
			}
			if corridor.Demo != "" {
				writeLine(pdf, Fact{Label: "Demographic line", Value: corridor.Demo, Source: "model_inference"})
			}
		}
	} else {
		for _, stop := range doc.Stops {
			pdf.Ln(2)
			pdf.SetFont("Helvetica", "B", 13)
			pdf.CellFormat(0, 8, sanitize(fmt.Sprintf("%d. %s", stop.Rank, stop.Name)), "", 1, "L", false, 0, "")
			writeLine(pdf, Fact{Label: "Door", Value: stop.Name, Source: "qloo_fact"})
			for _, score := range stop.Scores {
				writeLine(pdf, score)
			}
			if stop.SKU != "" {
				writeLine(pdf, Fact{Label: "SKU", Value: stop.SKU, Source: "model_inference"})
			}
			if stop.Pitch != "" {
				writeLine(pdf, Fact{Label: "Pitch", Value: stop.Pitch, Source: "model_inference"})
			}
			if len(stop.Cites) > 0 {
				writeLine(pdf, Fact{Label: "Citations", Value: join(stop.Cites), Source: "qloo_fact"})
			}
		}
	}
	if err := pdf.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeLegend(pdf *fpdf.Fpdf) {
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 5, "Qloo fact  -  roman type", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "I", 9)
	pdf.CellFormat(0, 5, "Model inference  -  italic type", "", 1, "L", false, 0, "")
}

func writeLine(pdf *fpdf.Fpdf, fact Fact) {
	style := ""
	prefix := "Qloo fact"
	switch fact.Source {
	case "model_inference":
		style = "I"
		prefix = "Model inference"
	case "google_fact":
		prefix = "Google fact"
	}
	pdf.SetFont("Helvetica", style, 10)
	pdf.MultiCell(0, 5, sanitize(prefix+": "+fact.Label+" - "+fact.Value), "", "L", false)
}

func join(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ", "
		}
		out += item
	}
	return out
}

func sanitize(s string) string {
	// Helvetica in this PDF build is a core font without the full Unicode set.
	var b bytes.Buffer
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 32 || r > 126:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
