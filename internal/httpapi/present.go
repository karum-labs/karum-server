package httpapi

import (
	"strconv"

	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/pdf"
)

type jobResponse struct {
	JobID        string `json:"job_id"`
	CampaignID   string `json:"campaign_id,omitempty"`
	State        string `json:"state"`
	TraceCursor  string `json:"trace_cursor,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type campaignResponse struct {
	JobID            string         `json:"job_id"`
	CampaignID       string         `json:"campaign_id,omitempty"`
	State            string         `json:"state"`
	Mode             string         `json:"mode"`
	City             string         `json:"city"`
	Point            *pointBody     `json:"point,omitempty"`
	PointAttribution string         `json:"point_attribution,omitempty"`
	RadiusMeters     int            `json:"radius_meters"`
	Brand            brandBody      `json:"brand"`
	Formula          string         `json:"formula,omitempty"`
	Doors            []doorBody     `json:"doors"`
	SkipList         []skipBody     `json:"skip_list"`
	Evidence         []evidenceBody `json:"evidence"`
	Baseline         baselineBody   `json:"baseline"`
	Heatmap          *heatmapBody   `json:"heatmap,omitempty"`
	Route            *routeBody     `json:"route,omitempty"`
	Warnings         []string       `json:"warnings,omitempty"`
	Trace            []traceBody    `json:"trace"`
	ErrorCode        string         `json:"error_code,omitempty"`
	ErrorMessage     string         `json:"error_message,omitempty"`
}

type brandBody struct {
	Name string    `json:"name"`
	SKUs []skuBody `json:"skus"`
}

type doorBody struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Rank        int          `json:"rank"`
	Scores      scoreSetBody `json:"scores"`
	SharedTags  []tagBody    `json:"shared_tags"`
	Place       *placeBody   `json:"place,omitempty"`
	Enrichment  *enrichBody  `json:"enrichment,omitempty"`
	Compare     *compareBody `json:"compare,omitempty"`
	Pitch       *pitchBody   `json:"pitch,omitempty"`
	Menu        *menuBody    `json:"menu,omitempty"`
	EvidenceIDs []string     `json:"evidence_ids"`
}

type menuBody struct {
	Summary         string          `json:"summary,omitempty"`
	ShelfUnknown    bool            `json:"shelf_unknown"`
	CompetitorCount int             `json:"competitor_count,omitempty"`
	Incumbents      []incumbentBody `json:"incumbents,omitempty"`
	Sources         []sourceBody    `json:"sources,omitempty"`
}

type incumbentBody struct {
	Name    string       `json:"name"`
	Sources []sourceBody `json:"sources,omitempty"`
}

type sourceBody struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type scoreSetBody struct {
	CulturalAffinity     *scoreBody `json:"cultural_affinity,omitempty"`
	ShelfOpportunity     *scoreBody `json:"shelf_opportunity,omitempty"`
	DemographicAlignment *scoreBody `json:"demographic_alignment,omitempty"`
	Composite            *scoreBody `json:"composite,omitempty"`
}

type scoreBody struct {
	Value   float64  `json:"value"`
	Display float64  `json:"display,omitempty"`
	Raw     float64  `json:"raw,omitempty"`
	Source  string   `json:"source"`
	Inputs  []string `json:"inputs,omitempty"`
}

type tagBody struct {
	URN   string `json:"urn"`
	Label string `json:"label"`
}

type enrichBody struct {
	Address     string  `json:"address,omitempty"`
	Hours       string  `json:"hours,omitempty"`
	Website     string  `json:"website,omitempty"`
	OpenStatus  string  `json:"open_status"`
	Unknown     bool    `json:"unknown"`
	Lat         float64 `json:"lat,omitempty"`
	Lng         float64 `json:"lng,omitempty"`
	Attribution string  `json:"attribution,omitempty"`
}

// placeBody is the recorded Qloo place block; it is not a Google Places read.
type placeBody struct {
	Address  string  `json:"address,omitempty"`
	Website  string  `json:"website,omitempty"`
	Phone    string  `json:"phone,omitempty"`
	Hours    string  `json:"hours,omitempty"`
	IsClosed bool    `json:"is_closed,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
}

type compareBody struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	Tags      []tagBody `json:"tags,omitempty"`
}

type pitchBody struct {
	Script        string      `json:"script"`
	SKU           skuBody     `json:"sku"`
	Battlecard    *battleBody `json:"battlecard,omitempty"`
	ModeParagraph string      `json:"mode_paragraph,omitempty"`
	Template      bool        `json:"template"`
}

type battleBody struct {
	Headline      string   `json:"headline"`
	IncumbentName string   `json:"incumbent_name"`
	TalkingPoints []string `json:"talking_points"`
}

type skipBody struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Reason          string  `json:"reason"`
	RawAffinity     float64 `json:"raw_affinity,omitempty"`
	Floor           float64 `json:"floor,omitempty"`
	CompetitorCount int     `json:"competitor_count,omitempty"`
	Lat             float64 `json:"lat,omitempty"`
	Lng             float64 `json:"lng,omitempty"`
}

type evidenceBody struct {
	ID          string   `json:"id"`
	Endpoint    string   `json:"endpoint"`
	Parameters  []string `json:"parameters,omitempty"`
	ResultCount int      `json:"result_count"`
	Source      string   `json:"source"`
}

type baselineBody struct {
	Text       string   `json:"text"`
	Ungrounded bool     `json:"ungrounded"`
	VenueNames []string `json:"venue_names,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
}

type heatmapBody struct {
	Available        bool           `json:"available"`
	Reason           string         `json:"reason,omitempty"`
	Corridors        []string       `json:"corridors,omitempty"`
	Anchors          []string       `json:"anchors,omitempty"`
	Cells            []heatmapCell  `json:"cells,omitempty"`
	CellCount        int            `json:"cell_count"`
	Localities       int            `json:"localities"`
	LocalityPolygons []localityBody `json:"locality_polygons,omitempty"`
}

type heatmapCell struct {
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	Affinity     float64 `json:"affinity,omitempty"`
	AffinityRank int     `json:"affinity_rank,omitempty"`
	Popularity   float64 `json:"popularity,omitempty"`
	Geohash      string  `json:"geohash,omitempty"`
}

type localityBody struct {
	Name     string      `json:"name"`
	Affinity float64     `json:"affinity,omitempty"`
	Centroid *pointBody  `json:"centroid,omitempty"`
	Polygon  []pointBody `json:"polygon,omitempty"`
}

type routeBody struct {
	Skipped     bool      `json:"skipped"`
	SkipReason  string    `json:"skip_reason,omitempty"`
	Failed      bool      `json:"failed"`
	Error       string    `json:"error,omitempty"`
	Polyline    string    `json:"polyline,omitempty"`
	StopIDs     []string  `json:"stop_ids,omitempty"`
	Legs        []legBody `json:"legs,omitempty"`
	Attribution string    `json:"attribution,omitempty"`
}

type legBody struct {
	FromID         string `json:"from_id"`
	ToID           string `json:"to_id"`
	Duration       string `json:"duration,omitempty"`
	Polyline       string `json:"polyline,omitempty"`
	DistanceMeters int    `json:"distance_meters"`
}

type traceBody struct {
	Sequence  int64  `json:"sequence"`
	Cursor    string `json:"cursor"`
	Stage     string `json:"stage"`
	ElapsedMS int64  `json:"elapsed_ms"`
	CacheHit  bool   `json:"cache_hit"`
	Detail    string `json:"detail"`
}

func jobOf(run *orchestrator.Run) jobResponse {
	cursor := "0"
	if n := len(run.Trace); n > 0 {
		cursor = run.Trace[n-1].Cursor
	}
	return jobResponse{
		JobID: run.ID, CampaignID: run.CampaignID, State: string(run.State),
		TraceCursor: cursor, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
	}
}

func campaignOf(run *orchestrator.Run) campaignResponse {
	out := campaignResponse{
		JobID: run.ID, CampaignID: run.CampaignID, State: string(run.State),
		Mode: string(run.Mode), City: run.City, RadiusMeters: run.RadiusMeters,
		PointAttribution: run.PointAttribution, Formula: run.Formula,
		Brand: brandBody{Name: run.Brand.Name},
		Baseline: baselineBody{
			Text: run.Baseline.Text, Ungrounded: true,
			VenueNames: run.Baseline.VenueNames, ErrorCode: run.Baseline.ErrorCode,
		},
		Warnings: run.Warnings, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
	}
	if run.Point.Lat != 0 || run.Point.Lng != 0 {
		out.Point = &pointBody{Lat: run.Point.Lat, Lng: run.Point.Lng}
	}
	for _, sku := range run.Brand.SKUs {
		out.Brand.SKUs = append(out.Brand.SKUs, skuBody{ID: sku.ID, Name: sku.Name})
	}
	for _, door := range run.Doors {
		out.Doors = append(out.Doors, doorOf(door, run.Pitches[door.Entity.ID], run.Findings[door.Entity.ID]))
	}
	if out.Doors == nil {
		out.Doors = []doorBody{}
	}
	for _, skip := range run.Skip {
		body := skipBody{
			ID: skip.Entity.ID, Name: skip.Entity.Name, Reason: skip.Reason,
			RawAffinity: skip.RawAffinity, Floor: skip.Floor, CompetitorCount: skip.CompetitorCount,
		}
		if skip.Entity.Place.HasLocation() {
			body.Lat = skip.Entity.Place.Lat
			body.Lng = skip.Entity.Place.Lng
		}
		out.SkipList = append(out.SkipList, body)
	}
	if out.SkipList == nil {
		out.SkipList = []skipBody{}
	}
	for _, call := range run.Evidence {
		out.Evidence = append(out.Evidence, evidenceBody{
			ID: call.ID, Endpoint: call.Endpoint, Parameters: call.Parameters,
			ResultCount: call.ResultCount, Source: "qloo_fact",
		})
	}
	if out.Evidence == nil {
		out.Evidence = []evidenceBody{}
	}
	for _, event := range run.Trace {
		out.Trace = append(out.Trace, traceBody{
			Sequence: event.Sequence, Cursor: event.Cursor, Stage: string(event.Stage),
			ElapsedMS: event.ElapsedMS, CacheHit: event.CacheHit, Detail: event.Detail,
		})
	}
	if out.Trace == nil {
		out.Trace = []traceBody{}
	}
	if run.Heatmap != nil {
		body := &heatmapBody{
			Available: run.Heatmap.Available, Reason: run.Heatmap.Reason,
			Corridors: run.Heatmap.Corridors, CellCount: run.Heatmap.CellCount,
			Localities: len(run.Heatmap.Localities),
		}
		for _, anchor := range run.Heatmap.Anchors {
			body.Anchors = append(body.Anchors, anchor.Name)
		}
		for _, cell := range run.Heatmap.Cells {
			body.Cells = append(body.Cells, heatmapCell{
				Lat: cell.Lat, Lng: cell.Lng, Affinity: cell.Affinity,
				AffinityRank: cell.AffinityRank, Popularity: cell.Popularity,
				Geohash: cell.Geohash,
			})
		}
		for _, locality := range run.Heatmap.Localities {
			item := localityBody{Name: locality.Name, Affinity: locality.Affinity}
			if locality.Centroid != nil {
				item.Centroid = &pointBody{Lat: locality.Centroid.Lat, Lng: locality.Centroid.Lng}
			}
			for _, vertex := range locality.Polygon {
				item.Polygon = append(item.Polygon, pointBody{Lat: vertex.Lat, Lng: vertex.Lng})
			}
			body.LocalityPolygons = append(body.LocalityPolygons, item)
		}
		out.Heatmap = body
	}
	if run.Route != nil {
		body := &routeBody{
			Skipped: run.Route.Skipped, SkipReason: run.Route.SkipReason,
			Failed: run.Route.Failed, Error: run.Route.Error, Polyline: run.Route.Polyline,
			StopIDs: run.Route.StopIDs, Attribution: run.Route.Attribution,
		}
		for _, leg := range run.Route.Legs {
			body.Legs = append(body.Legs, legBody{
				FromID: leg.FromID, ToID: leg.ToID, Duration: leg.Duration,
				Polyline: leg.Polyline, DistanceMeters: leg.DistanceMeters,
			})
		}
		out.Route = body
	}
	return out
}

func doorOf(door orchestrator.Door, pitch orchestrator.Pitch, finding orchestrator.AuditFinding) doorBody {
	body := doorBody{
		ID: door.Entity.ID, Name: door.Entity.Name, Rank: door.Rank,
		EvidenceIDs: door.EvidenceIDs,
		Scores: scoreSetBody{
			CulturalAffinity: scoreOf(door.Affinity, "model_inference", false),
		},
	}
	if body.Scores.CulturalAffinity != nil {
		body.Scores.CulturalAffinity.Raw = door.Affinity.RawValue
		body.Scores.CulturalAffinity.Source = "model_inference"
	}
	if door.ShelfOpportunity != nil {
		body.Scores.ShelfOpportunity = scoreOf(*door.ShelfOpportunity, "model_inference", false)
	}
	if door.DemographicAlignment != nil {
		body.Scores.DemographicAlignment = scoreOf(*door.DemographicAlignment, "model_inference", false)
	}
	if door.Composite != nil {
		shown := scoreOf(*door.Composite, "model_inference", true)
		body.Scores.Composite = shown
	}
	for _, tag := range door.SharedTags {
		body.SharedTags = append(body.SharedTags, tagBody{URN: tag.URN, Label: tag.Label})
	}
	if body.SharedTags == nil {
		body.SharedTags = []tagBody{}
	}
	if door.Enrichment != nil {
		body.Enrichment = &enrichBody{
			Address: door.Enrichment.Address, Hours: door.Enrichment.Hours,
			Website: door.Enrichment.Website, OpenStatus: door.Enrichment.OpenStatus,
			Unknown: door.Enrichment.Unknown, Lat: door.Enrichment.Lat, Lng: door.Enrichment.Lng,
			Attribution: door.Enrichment.Attribution,
		}
	}
	if door.Entity.Place != nil {
		body.Place = &placeBody{
			Address: door.Entity.Place.Address, Website: door.Entity.Place.Website,
			Phone: door.Entity.Place.Phone, Hours: door.Entity.Place.Hours,
			IsClosed: door.Entity.Place.IsClosed,
			Lat:      door.Entity.Place.Lat, Lng: door.Entity.Place.Lng,
		}
	}
	if door.Compare != nil {
		chart := &compareBody{Available: door.Compare.Available, Reason: door.Compare.Reason}
		for _, tag := range door.Compare.Tags {
			chart.Tags = append(chart.Tags, tagBody{URN: tag.URN, Label: tag.Label})
		}
		body.Compare = chart
	}
	if finding.Succeeded || finding.ShelfUnknown || finding.MenuSummary != "" || len(finding.Incumbents) > 0 {
		menu := &menuBody{
			Summary:         finding.MenuSummary,
			ShelfUnknown:    finding.ShelfUnknown,
			CompetitorCount: finding.CompetitorCount,
		}
		for _, incumbent := range finding.Incumbents {
			item := incumbentBody{Name: incumbent.Name}
			for _, source := range incumbent.Sources {
				item.Sources = append(item.Sources, sourceBody{URL: source.URL, Title: source.Title})
			}
			menu.Incumbents = append(menu.Incumbents, item)
		}
		for _, source := range finding.Sources {
			menu.Sources = append(menu.Sources, sourceBody{URL: source.URL, Title: source.Title})
		}
		body.Menu = menu
	}
	if pitch.Script != "" || pitch.Template {
		item := &pitchBody{
			Script: pitch.Script, Template: pitch.Template, ModeParagraph: pitch.ModeParagraph,
			SKU: skuBody{ID: pitch.RecommendedSKU.ID, Name: pitch.RecommendedSKU.Name},
		}
		if pitch.Battlecard != nil {
			item.Battlecard = &battleBody{
				Headline: pitch.Battlecard.Headline, IncumbentName: pitch.Battlecard.IncumbentName,
				TalkingPoints: pitch.Battlecard.TalkingPoints,
			}
		}
		body.Pitch = item
	}
	return body
}

func scoreOf(score orchestrator.SubScore, source string, display bool) *scoreBody {
	body := &scoreBody{Value: score.Value, Raw: score.RawValue, Source: source, Inputs: score.Inputs}
	if display {
		body.Display = score.RawValue
	}
	return body
}

func documentOf(run *orchestrator.Run, brief bool) pdf.Document {
	doc := pdf.Document{
		Brand: run.Brand.Name, City: run.City, Mode: string(run.Mode), Formula: run.Formula,
	}
	if brief && run.Heatmap != nil {
		for _, name := range run.Heatmap.Corridors {
			corridor := pdf.Corridor{Name: name}
			for _, anchor := range run.Heatmap.Anchors {
				corridor.Anchors = append(corridor.Anchors, anchor.Name)
			}
			doc.Corridors = append(doc.Corridors, corridor)
		}
	}
	for _, door := range run.Doors {
		pitch := run.Pitches[door.Entity.ID]
		stop := pdf.Stop{Rank: door.Rank, Name: door.Entity.Name, SKU: pitch.RecommendedSKU.Name, Pitch: pitch.Script, Cites: door.EvidenceIDs}
		stop.Scores = append(stop.Scores, pdf.Fact{Label: "Cultural affinity", Value: formatScore(door.Affinity), Source: "model_inference"})
		if door.Affinity.RawValue > 0 {
			stop.Scores = append(stop.Scores, pdf.Fact{Label: "Raw affinity", Value: formatScore(orchestrator.SubScore{Value: door.Affinity.RawValue}), Source: "qloo_fact"})
		}
		if door.ShelfOpportunity != nil {
			stop.Scores = append(stop.Scores, pdf.Fact{Label: "Shelf opportunity", Value: formatScore(*door.ShelfOpportunity), Source: "model_inference"})
		}
		if door.DemographicAlignment != nil {
			stop.Scores = append(stop.Scores, pdf.Fact{Label: "Demographic alignment", Value: formatScore(*door.DemographicAlignment), Source: "model_inference"})
		}
		if door.Enrichment != nil && door.Enrichment.Address != "" {
			stop.Scores = append(stop.Scores, pdf.Fact{Label: "Address", Value: door.Enrichment.Address, Source: "google_fact"})
		}
		doc.Stops = append(doc.Stops, stop)
	}
	return doc
}

func formatScore(score orchestrator.SubScore) string {
	return strconv.FormatFloat(score.Value, 'f', 2, 64)
}
