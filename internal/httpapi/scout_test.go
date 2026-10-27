package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/pdf"
	"github.com/karum-labs/karum-server/internal/presets"
	"github.com/karum-labs/karum-server/internal/resilience"
)

type httpTaste struct {
	mu    sync.Mutex
	calls int
	name  string
	block chan struct{}
}

func (t *httpTaste) ScoutDoors(ctx context.Context, _ orchestrator.ScoutRequest) (orchestrator.ScoutResult, error) {
	t.mu.Lock()
	t.calls++
	name := t.name
	t.mu.Unlock()
	if t.block != nil {
		select {
		case <-ctx.Done():
			return orchestrator.ScoutResult{}, ctx.Err()
		case <-t.block:
		}
	}
	return orchestrator.ScoutResult{
		Doors: []orchestrator.Door{{
			Entity: orchestrator.SeedEntity{
				ID: "d-1", Type: "urn:entity:place", Name: name,
				Place: &orchestrator.PlaceFacts{
					Address: "1100 E 5th St, Austin, TX",
					Website: "https://example.com/door",
					Hours:   "Mon 17:00-23:00",
					Phone:   "+1 512 555 0100",
					Lat:     30.26, Lng: -97.73,
				},
			},
			Rank:        1,
			Affinity:    orchestrator.SubScore{Name: "cultural_affinity", Value: 1, RawValue: 0.91},
			EvidenceIDs: []string{"ev-0001"},
			Explainability: []orchestrator.Explainability{
				{EntityID: "e-1", Label: "Ilegal Mezcal", Score: 0.62},
			},
			Demographics: &orchestrator.Demographics{
				AgeBuckets:  []orchestrator.DemographicBucket{{Label: "25_34", Delta: 0.12}},
				MaleDelta:   0.03,
				FemaleDelta: -0.04,
			},
			DistanceMeters: 820,
			Lat:            30.26,
			Lng:            -97.73,
		}},
		Skip: []orchestrator.SkipItem{{
			Entity: orchestrator.SeedEntity{
				ID: "d-2", Type: "urn:entity:place", Name: "Below Floor",
				Place: &orchestrator.PlaceFacts{
					Address: "1200 E 6th St, Austin, TX",
					Lat:     30.27, Lng: -97.72,
				},
			},
			Reason:      "low_affinity",
			RawAffinity: 0.2,
			Floor:       0.4,
		}},
		Evidence: []orchestrator.EvidenceCall{{ID: "ev-0001", Endpoint: "/v2/insights", ResultCount: 1}},
	}, nil
}

func (t *httpTaste) n() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type httpAgent struct{}

func (httpAgent) AuditMenu(context.Context, orchestrator.AuditRequest) (orchestrator.AuditResult, error) {
	return orchestrator.AuditResult{}, nil
}
func (httpAgent) DraftDoorPitch(context.Context, orchestrator.DraftRequest) (orchestrator.DraftResult, error) {
	return orchestrator.DraftResult{Script: "Hello (ev-0001).", RecommendedSKU: orchestrator.SKU{ID: "sku", Name: "Espadin"}}, nil
}
func (httpAgent) LlmOnlyBaseline(context.Context, orchestrator.BaselineRequest) (orchestrator.BaselineResult, error) {
	return orchestrator.BaselineResult{Text: "ungrounded", VenueNames: []string{"Somewhere"}}, nil
}

type httpGeo struct {
	calls  int
	result *geocode.Result
	err    error
}

func (g *httpGeo) Geocode(context.Context, string) (*geocode.Result, error) {
	g.calls++
	return g.result, g.err
}

type boomPDF struct{}

func (boomPDF) RunSheet(pdf.Document) ([]byte, error) { return nil, errors.New("render failed") }
func (boomPDF) SiteBrief(pdf.Document) ([]byte, error) {
	return nil, errors.New("render failed")
}

func testStack(mode config.Mode) (*httpTaste, *httpTaste, *httptest.Server, *orchestrator.Store) {
	store := orchestrator.NewStore()
	liveTaste := &httpTaste{name: "Live Door"}
	fixtureTaste := &httpTaste{name: "Fixture Door"}
	live := orchestrator.NewRunner(liveTaste, httpAgent{}, store)
	fixture := orchestrator.NewRunner(fixtureTaste, httpAgent{}, store)
	router := NewRouterWithDeps(&config.Config{Mode: mode, Version: "test"}, Dependencies{
		Live: live, Fixture: fixture, Store: store,
		Geocoder: &httpGeo{result: &geocode.Result{
			Resolved:         geocode.Point{Lat: 30.26, Lng: -97.74},
			FormattedAddress: "Austin, TX", Attribution: geocode.AttributionGoogle,
		}},
	})
	return liveTaste, fixtureTaste, httptest.NewServer(router), store
}

func postJSON(t *testing.T, srv *httptest.Server, path, key string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScoutCreatesAJobAndGetReturnsTheCampaign(t *testing.T) {
	_, fixture, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()
	resp := postJSON(t, srv, "/api/v1/scout", "k1", map[string]any{"preset_id": "austin-wholesale"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	created := decode(t, resp)
	id, _ := created["job_id"].(string)
	if id == "" || created["state"] != "ready" && created["state"] != "degraded" {
		t.Fatalf("job = %+v", created)
	}
	if fixture.n() != 1 {
		t.Fatalf("fixture calls = %d", fixture.n())
	}
	get, err := http.Get(srv.URL + "/api/v1/scout/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, get)
	doors, _ := body["doors"].([]any)
	if len(doors) != 1 {
		t.Fatalf("campaign = %+v", body)
	}
	baseline, _ := body["baseline"].(map[string]any)
	if baseline["ungrounded"] != true {
		t.Fatalf("baseline = %+v", baseline)
	}
}

// A campaign header exists (it was seeded by a preset) but no run was ever
// started for it: the gateway must answer 404, not invent an "absent" state.
func TestGetCampaignWithNoRunReturnsNotFound(t *testing.T) {
	_, _, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()
	get, err := http.Get(srv.URL + "/api/v1/campaigns/cmp-austin-wholesale")
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, wanted 404", get.StatusCode)
	}
	body := decode(t, get)
	if body["error"] != "not_found" || body["message"] != "no run for this campaign" {
		t.Fatalf("body = %+v", body)
	}
}

func TestPresetReplaySkipsThePipelineAndFreeEntryStaysLive(t *testing.T) {
	live, fixture, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()
	first := decode(t, postJSON(t, srv, "/api/v1/scout", "p1", map[string]any{"preset_id": "austin-wholesale"}))
	second := decode(t, postJSON(t, srv, "/api/v1/scout", "p2", map[string]any{"preset_id": "austin-wholesale"}))
	if first["job_id"] != second["job_id"] {
		t.Fatalf("replay job = %v, first = %v", second["job_id"], first["job_id"])
	}
	if fixture.n() != 1 {
		t.Fatalf("fixture calls after replay = %d", fixture.n())
	}
	rerun := decode(t, postJSON(t, srv, "/api/v1/scout", "p3", map[string]any{"preset_id": "austin-wholesale", "rerun": true}))
	if rerun["job_id"] == first["job_id"] || fixture.n() != 2 {
		t.Fatalf("rerun = %+v calls = %d", rerun, fixture.n())
	}
	free := decode(t, postJSON(t, srv, "/api/v1/scout", "p4", map[string]any{
		"free_entry": true, "city": "Austin", "mode": "wholesale",
		"brand_name": "Sola", "point": map[string]any{"lat": 30.26, "lng": -97.74},
		"seed_entities": []map[string]string{{"id": "seed-1", "type": "urn:entity:brand", "name": "Sola"}},
	}))
	if live.n() != 1 {
		t.Fatalf("live calls = %d body = %+v", live.n(), free)
	}
	got, err := http.Get(srv.URL + "/api/v1/scout/" + free["job_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	campaign := decode(t, got)
	doors := campaign["doors"].([]any)
	door := doors[0].(map[string]any)
	if door["name"] != "Live Door" {
		t.Fatalf("free entry door = %+v", door)
	}
}

// PLAN.md §49: a preset that belongs to the judge account is not readable
// by anonymous visitors through the shared campaign id. The judge sees it;
// unowned presets (test harness, local dev before seeding) stay public.
func TestOwnedPresetCampaignIsHiddenFromAnonymousVisitors(t *testing.T) {
	_, _, srv, store := testStack(config.ModeFixture)
	defer srv.Close()
	store.LinkPresets(presets.JudgeAccountID, []string{presets.CampaignID("austin-wholesale")})
	// A judge-owned preset answers 404 to an anonymous GET even once a run
	// exists, so a landing visitor cannot read the owner's prior attempt.
	first := decode(t, postJSON(t, srv, "/api/v1/scout", "owned-1", map[string]any{"preset_id": "austin-wholesale"}))
	if first["job_id"] == nil {
		t.Fatalf("first preset = %+v", first)
	}
	anon, err := http.Get(srv.URL + "/api/v1/campaigns/" + presets.CampaignID("austin-wholesale"))
	if err != nil {
		t.Fatal(err)
	}
	if anon.StatusCode != http.StatusNotFound {
		t.Fatalf("anonymous owned-campaign GET = %d, want 404", anon.StatusCode)
	}
	// Un-owned preset still answers 200.
	run := decode(t, postJSON(t, srv, "/api/v1/scout", "open-1", map[string]any{"preset_id": "brooklyn-flagship"}))
	if run["job_id"] == nil {
		t.Fatalf("unowned preset = %+v", run)
	}
	pub, err := http.Get(srv.URL + "/api/v1/campaigns/" + presets.CampaignID("brooklyn-flagship"))
	if err != nil {
		t.Fatal(err)
	}
	if pub.StatusCode != http.StatusOK {
		t.Fatalf("unowned-campaign GET = %d, want 200", pub.StatusCode)
	}
}

// PLAN.md §49: two anonymous preset starts must not share a job id, so one
// visitor's landing never shows another visitor's run (the previous bug
// where every anonymous click replayed the judge's stored run).
func TestAnonymousPresetStartsDoNotShareRuns(t *testing.T) {
	_, fixture, srv, store := testStack(config.ModeFixture)
	defer srv.Close()
	store.LinkPresets(presets.JudgeAccountID, []string{presets.CampaignID("austin-wholesale")})
	first := decode(t, postJSON(t, srv, "/api/v1/scout", "anon-1", map[string]any{"preset_id": "austin-wholesale"}))
	second := decode(t, postJSON(t, srv, "/api/v1/scout", "anon-2", map[string]any{"preset_id": "austin-wholesale"}))
	if first["job_id"] == nil || first["job_id"] == second["job_id"] {
		t.Fatalf("anonymous runs share job id: first=%v second=%v", first["job_id"], second["job_id"])
	}
	// A separate cmp-NNNN campaign per start, not the shared preset id.
	if first["campaign_id"] == presets.CampaignID("austin-wholesale") || first["campaign_id"] == second["campaign_id"] {
		t.Fatalf("anonymous campaign ids clashed: first=%v second=%v", first["campaign_id"], second["campaign_id"])
	}
	// Each anonymous start runs the pipeline; it does not replay the owner's.
	if fixture.n() < 2 {
		t.Fatalf("fixture calls = %d, want at least 2 (one per anonymous start)", fixture.n())
	}
}

func TestIdempotencyValidationAndAmbiguousGeocode(t *testing.T) {
	_, _, srv, _ := testStack(config.ModeLive)
	defer srv.Close()
	missing := postJSON(t, srv, "/api/v1/scout", "", map[string]any{"preset_id": "austin-wholesale"})
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing key = %d", missing.StatusCode)
	}
	missing.Body.Close()

	body := map[string]any{
		"city": "Austin", "mode": "wholesale", "brand_name": "Sola",
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
		"point":         map[string]any{"lat": 30.2, "lng": -97.7},
	}
	first := decode(t, postJSON(t, srv, "/api/v1/scout", "same", body))
	again := decode(t, postJSON(t, srv, "/api/v1/scout", "same", body))
	if first["job_id"] != again["job_id"] {
		t.Fatalf("idempotent jobs differ: %v vs %v", first["job_id"], again["job_id"])
	}
	conflict := postJSON(t, srv, "/api/v1/scout", "same", map[string]any{"preset_id": "brooklyn-flagship"})
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict = %d", conflict.StatusCode)
	}
	conflict.Body.Close()

	badRadius := postJSON(t, srv, "/api/v1/scout", "rad", map[string]any{
		"city": "Austin", "radius_meters": 10,
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
	})
	if badRadius.StatusCode != http.StatusBadRequest {
		t.Fatalf("radius = %d", badRadius.StatusCode)
	}
	badRadius.Body.Close()

	badPoint := postJSON(t, srv, "/api/v1/scout", "pt", map[string]any{
		"city": "Austin", "point": map[string]any{"lat": 120, "lng": 0},
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
	})
	if badPoint.StatusCode != http.StatusBadRequest {
		t.Fatalf("point = %d", badPoint.StatusCode)
	}
	badPoint.Body.Close()

	geo := &httpGeo{result: &geocode.Result{Candidates: []geocode.Candidate{
		{FormattedAddress: "Austin, TX", Point: geocode.Point{Lat: 30.2, Lng: -97.7}},
		{FormattedAddress: "Austin, MN", Point: geocode.Point{Lat: 43.6, Lng: -92.9}},
	}}}
	store := orchestrator.NewStore()
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeLive, Version: "test"}, Dependencies{
		Live:  orchestrator.NewRunner(&httpTaste{name: "Should Not Run"}, httpAgent{}, store),
		Store: store, Geocoder: geo,
	})
	amb := httptest.NewServer(router)
	defer amb.Close()
	resp := postJSON(t, amb, "/api/v1/scout", "amb", map[string]any{
		"city": "Austin", "brand_name": "Sola",
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguous = %d", resp.StatusCode)
	}
	payload := decode(t, resp)
	cands, _ := payload["candidates"].([]any)
	if payload["error"] != "ambiguous" || len(cands) != 2 {
		t.Fatalf("ambiguous body = %+v", payload)
	}
}

func TestDraftsEventsCampaignAndPDFFailure(t *testing.T) {
	_, _, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()
	created := decode(t, postJSON(t, srv, "/api/v1/scout", "d1", map[string]any{"preset_id": "austin-wholesale"}))
	id := created["job_id"].(string)

	sheet, err := http.Post(srv.URL+"/api/v1/scout/"+id+"/drafts/run-sheet", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(sheet.Body)
	sheet.Body.Close()
	if sheet.StatusCode != http.StatusOK || !bytes.HasPrefix(raw, []byte("%PDF")) {
		t.Fatalf("run-sheet status=%d body=%q", sheet.StatusCode, raw[:min(40, len(raw))])
	}

	brief, err := http.Post(srv.URL+"/api/v1/scout/"+id+"/drafts/site-brief", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	brief.Body.Close()
	if brief.StatusCode != http.StatusBadRequest {
		t.Fatalf("wholesale brief = %d", brief.StatusCode)
	}

	flagship := decode(t, postJSON(t, srv, "/api/v1/scout", "d2", map[string]any{"preset_id": "brooklyn-flagship"}))
	fid := flagship["job_id"].(string)
	site, err := http.Post(srv.URL+"/api/v1/scout/"+fid+"/drafts/site-brief", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	siteRaw, _ := io.ReadAll(site.Body)
	site.Body.Close()
	if site.StatusCode != http.StatusOK || !bytes.Contains(siteRaw, []byte("Brooklyn")) {
		t.Fatalf("site brief status=%d", site.StatusCode)
	}

	events, err := http.Get(srv.URL + "/api/v1/scout/" + id + "/events?cursor=1")
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(events.Body)
	events.Body.Close()
	if !strings.Contains(string(stream), "event: trace") || !strings.Contains(string(stream), "scout") {
		t.Fatalf("sse = %s", stream)
	}

	campaign, err := http.Get(srv.URL + "/api/v1/campaigns/cmp-austin-wholesale")
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, campaign)
	if got["campaign_id"] != "cmp-austin-wholesale" {
		t.Fatalf("campaign = %+v", got)
	}

	store := orchestrator.NewStore()
	runner := orchestrator.NewRunner(&httpTaste{name: "Fixture Door"}, httpAgent{}, store)
	broken := httptest.NewServer(NewRouterWithDeps(&config.Config{Mode: config.ModeFixture}, Dependencies{
		Fixture: runner, Store: store, Renderer: boomPDF{},
	}))
	defer broken.Close()
	job := decode(t, postJSON(t, broken, "/api/v1/scout", "pdf", map[string]any{"preset_id": "austin-wholesale"}))
	fail, err := http.Post(broken.URL+"/api/v1/scout/"+job["job_id"].(string)+"/drafts/run-sheet", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	fail.Body.Close()
	if fail.StatusCode != http.StatusBadGateway {
		t.Fatalf("pdf failure = %d", fail.StatusCode)
	}
	still, err := http.Get(broken.URL + "/api/v1/scout/" + job["job_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	kept := decode(t, still)
	if len(kept["doors"].([]any)) != 1 {
		t.Fatalf("campaign lost after pdf failure: %+v", kept)
	}
}

func TestAbortAndRateLimit(t *testing.T) {
	store := orchestrator.NewStore()
	block := make(chan struct{})
	taste := &httpTaste{name: "Slow", block: block}
	runner := orchestrator.NewRunner(taste, httpAgent{}, store)
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeLive}, Dependencies{
		Live: runner, Store: store, Async: true,
		Limiter:  resilience.NewLimiter(0.01, 1, 0.01, 1),
		Geocoder: &httpGeo{result: &geocode.Result{Resolved: geocode.Point{Lat: 1, Lng: 2}, Attribution: geocode.AttributionGoogle}},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := postJSON(t, srv, "/api/v1/scout", "slow", map[string]any{
		"city": "Austin", "mode": "wholesale", "brand_name": "Sola",
		"point":         map[string]any{"lat": 30.2, "lng": -97.7},
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("async = %d", resp.StatusCode)
	}
	job := decode(t, resp)
	abort, err := http.Post(srv.URL+"/api/v1/scout/"+job["job_id"].(string)+"/abort", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	abort.Body.Close()
	if abort.StatusCode != http.StatusAccepted {
		t.Fatalf("abort = %d", abort.StatusCode)
	}
	limited := postJSON(t, srv, "/api/v1/scout", "again", map[string]any{"preset_id": "austin-wholesale"})
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit = %d", limited.StatusCode)
	}
	limited.Body.Close()
	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, ok := store.Get(job["job_id"].(string))
		if ok && run.State == orchestrator.StateFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("aborted job did not fail")
}

func TestRegenerateIsRateLimited(t *testing.T) {
	store := orchestrator.NewStore()
	runner := orchestrator.NewRunner(&httpTaste{name: "Live"}, httpAgent{}, store)
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeLive}, Dependencies{
		Live: runner, Store: store,
		RegenLimiter: resilience.NewLimiter(0.01, 1, 100, 100),
		Geocoder: &httpGeo{result: &geocode.Result{
			Resolved:         geocode.Point{Lat: 30.26, Lng: -97.74},
			FormattedAddress: "Austin, TX", Attribution: geocode.AttributionGoogle,
		}},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	created := postJSON(t, srv, "/api/v1/scout", "regen-1", map[string]any{
		"city": "Austin", "mode": "wholesale", "brand_name": "Sola",
		"point":         map[string]any{"lat": 30.2, "lng": -97.7},
		"seed_entities": []map[string]string{{"id": "s", "type": "urn:entity:brand", "name": "Sola"}},
	})
	job := decode(t, created)
	path := "/api/v1/scout/" + job["job_id"].(string) + "/drafts/doors/d-1"

	first := postJSON(t, srv, path, "", nil)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first regenerate = %d", first.StatusCode)
	}
	second := postJSON(t, srv, path, "", nil)
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("regenerate rate limit = %d, want 429", second.StatusCode)
	}
}

type payloadHeatmap struct{}

func (payloadHeatmap) GetLocalityHeatmap(context.Context, orchestrator.HeatmapQuery) (orchestrator.HeatmapView, error) {
	return orchestrator.HeatmapView{
		Available: true,
		Corridors: []string{"Williamsburg"},
		CellCount: 1,
		Cells: []orchestrator.HeatmapCell{{
			Lat: 40.71, Lng: -73.96, Affinity: 0.8, AffinityRank: 3, Popularity: 0.4, Geohash: "dr5ru",
		}},
		Localities: []orchestrator.LocalityPolygon{{
			Name: "Williamsburg", Affinity: 0.8,
			Centroid: &orchestrator.Point{Lat: 40.71, Lng: -73.96},
			Polygon: []orchestrator.Point{
				{Lat: 40.70, Lng: -73.97},
				{Lat: 40.72, Lng: -73.97},
				{Lat: 40.72, Lng: -73.95},
			},
		}},
	}, nil
}

type payloadAgent struct{}

func (payloadAgent) LlmOnlyBaseline(context.Context, orchestrator.BaselineRequest) (orchestrator.BaselineResult, error) {
	return orchestrator.BaselineResult{Text: "ungrounded", VenueNames: []string{"Somewhere"}}, nil
}

func (payloadAgent) AuditMenu(context.Context, orchestrator.AuditRequest) (orchestrator.AuditResult, error) {
	return orchestrator.AuditResult{Findings: []orchestrator.AuditFinding{{
		Door:            orchestrator.SeedEntity{ID: "d-1", Name: "Menu Door"},
		Succeeded:       true,
		ShelfUnknown:    true,
		CompetitorCount: 2,
		MenuSummary:     "Two mezcal competitors on the list.",
		Incumbents: []orchestrator.Incumbent{{
			Name:    "Del Maguey",
			Sources: []orchestrator.Source{{URL: "https://example.com/menu", Title: "Menu"}},
		}},
		Sources: []orchestrator.Source{{URL: "https://example.com/list", Title: "List"}},
	}}}, nil
}

func (payloadAgent) DraftDoorPitch(context.Context, orchestrator.DraftRequest) (orchestrator.DraftResult, error) {
	return orchestrator.DraftResult{Script: "Grounded pitch (ev-0001)."}, nil
}

func TestCampaignPayloadCarriesMenuFindingsAndHeatmapCells(t *testing.T) {
	store := orchestrator.NewStore()
	runner := orchestrator.NewRunner(&httpTaste{name: "Menu Door"}, payloadAgent{}, store).
		WithHeatmap(payloadHeatmap{})
	srv := httptest.NewServer(NewRouterWithDeps(&config.Config{Mode: config.ModeFixture, Version: "test"}, Dependencies{
		Fixture: runner, Store: store,
	}))
	defer srv.Close()

	created := decode(t, postJSON(t, srv, "/api/v1/scout", "menu-cells", map[string]any{"preset_id": "brooklyn-flagship"}))
	id, _ := created["job_id"].(string)
	if id == "" {
		t.Fatalf("job = %+v", created)
	}
	get, err := http.Get(srv.URL + "/api/v1/scout/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, get)

	doors, _ := body["doors"].([]any)
	if len(doors) != 1 {
		t.Fatalf("doors = %+v", body["doors"])
	}
	door, _ := doors[0].(map[string]any)
	menu, ok := door["menu"].(map[string]any)
	if !ok {
		t.Fatalf("door has no menu block: %+v", door)
	}
	if menu["shelf_unknown"] != true || menu["competitor_count"] != float64(2) {
		t.Fatalf("menu = %+v", menu)
	}
	if menu["summary"] != "Two mezcal competitors on the list." {
		t.Fatalf("menu summary = %v", menu["summary"])
	}
	incumbents, _ := menu["incumbents"].([]any)
	if len(incumbents) != 1 {
		t.Fatalf("incumbents = %+v", menu["incumbents"])
	}
	incumbent, _ := incumbents[0].(map[string]any)
	if incumbent["name"] != "Del Maguey" {
		t.Fatalf("incumbent = %+v", incumbent)
	}
	sources, _ := incumbent["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("incumbent sources = %+v", incumbent["sources"])
	}
	source, _ := sources[0].(map[string]any)
	if source["url"] != "https://example.com/menu" || source["title"] != "Menu" {
		t.Fatalf("source = %+v", source)
	}

	heatmap, ok := body["heatmap"].(map[string]any)
	if !ok {
		t.Fatalf("campaign has no heatmap: %+v", body)
	}
	cells, _ := heatmap["cells"].([]any)
	if len(cells) != 1 {
		t.Fatalf("cells = %+v", heatmap["cells"])
	}
	cell, _ := cells[0].(map[string]any)
	if cell["lat"] != 40.71 || cell["lng"] != -73.96 || cell["affinity"] != 0.8 || cell["affinity_rank"] != float64(3) {
		t.Fatalf("cell = %+v", cell)
	}
	localities, _ := heatmap["locality_polygons"].([]any)
	if len(localities) != 1 {
		t.Fatalf("locality_polygons = %+v", heatmap["locality_polygons"])
	}
	locality, _ := localities[0].(map[string]any)
	polygon, _ := locality["polygon"].([]any)
	if locality["name"] != "Williamsburg" || len(polygon) != 3 {
		t.Fatalf("locality = %+v", locality)
	}
	place, ok := door["place"].(map[string]any)
	if !ok || place["address"] != "1100 E 5th St, Austin, TX" ||
		place["website"] != "https://example.com/door" ||
		place["lat"] != 30.26 || place["lng"] != -97.73 {
		t.Fatalf("door place = %+v", door["place"])
	}
	skips, _ := body["skip_list"].([]any)
	if len(skips) != 1 {
		t.Fatalf("skip_list = %+v", body["skip_list"])
	}
	skip, _ := skips[0].(map[string]any)
	if skip["lat"] != 30.27 || skip["lng"] != -97.72 {
		t.Fatalf("skip point = %+v", skip)
	}
}

func TestUniqueSkipsDropsExactDuplicates(t *testing.T) {
	skip := orchestrator.SkipItem{
		Entity: orchestrator.SeedEntity{
			ID: "d-2", Type: "urn:entity:place", Name: "Below Floor",
		},
		Reason: "low_affinity",
	}
	closed := skip
	closed.Reason = "closed"

	got := uniqueSkips([]orchestrator.SkipItem{skip, skip, closed})
	if len(got) != 2 {
		t.Fatalf("uniqueSkips() kept %d skips, want 2: %+v", len(got), got)
	}
	if got[0].Reason != "low_affinity" || got[1].Reason != "closed" {
		t.Fatalf("uniqueSkips() = %+v", got)
	}
}

// The gateway must not drop the Qloo evidence service-taste computed: the
// explainability contributions, the demographic profile, and the distance
// are what the "How we know" drawer renders (PLAN.md §34).
func TestCampaignPayloadCarriesTheQlooEvidence(t *testing.T) {
	_, _, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()
	created := decode(t, postJSON(t, srv, "/api/v1/scout", "evidence-1", map[string]any{"preset_id": "austin-wholesale"}))
	id, _ := created["job_id"].(string)
	if id == "" {
		t.Fatalf("job = %+v", created)
	}
	get, err := http.Get(srv.URL + "/api/v1/scout/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, get)
	doors, _ := body["doors"].([]any)
	if len(doors) != 1 {
		t.Fatalf("doors = %+v", body["doors"])
	}
	door, _ := doors[0].(map[string]any)

	contributions, _ := door["explainability"].([]any)
	if len(contributions) != 1 {
		t.Fatalf("explainability = %+v", door["explainability"])
	}
	contribution, _ := contributions[0].(map[string]any)
	if contribution["entity_id"] != "e-1" || contribution["label"] != "Ilegal Mezcal" ||
		contribution["score"] != 0.62 {
		t.Fatalf("contribution = %+v", contribution)
	}

	demo, ok := door["demographics"].(map[string]any)
	if !ok {
		t.Fatalf("door has no demographics: %+v", door)
	}
	buckets, _ := demo["age_buckets"].([]any)
	if len(buckets) != 1 {
		t.Fatalf("age_buckets = %+v", demo["age_buckets"])
	}
	bucket, _ := buckets[0].(map[string]any)
	if bucket["label"] != "25_34" || bucket["delta"] != 0.12 {
		t.Fatalf("bucket = %+v", bucket)
	}
	if demo["male_delta"] != 0.03 || demo["female_delta"] != -0.04 {
		t.Fatalf("deltas = %+v", demo)
	}
	if door["distance_meters"] != float64(820) {
		t.Fatalf("distance_meters = %+v", door["distance_meters"])
	}
}

func TestHealthReportsDependencies(t *testing.T) {
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeFixture, Version: "test"}, Dependencies{
		Probes: Probes{
			Taste:    func(context.Context) string { return "fixture" },
			AI:       func(context.Context) string { return "degraded" },
			Redis:    func(context.Context) string { return "live" },
			Postgres: func(context.Context) string { return "down" },
			QlooMode: func() string { return "fixture" },
		},
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	var got HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Dependencies.Taste != "fixture" || got.Dependencies.AI != "degraded" || got.Dependencies.Postgres != "down" || got.Dependencies.QlooMode != "fixture" {
		t.Fatalf("health = %+v", got)
	}
}
