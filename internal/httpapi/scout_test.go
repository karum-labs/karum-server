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
			Entity:      orchestrator.SeedEntity{ID: "d-1", Type: "urn:entity:place", Name: name},
			Rank:        1,
			Affinity:    orchestrator.SubScore{Name: "cultural_affinity", Value: 1, RawValue: 0.91},
			EvidenceIDs: []string{"ev-0001"},
			Lat:         30.26,
			Lng:         -97.73,
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
