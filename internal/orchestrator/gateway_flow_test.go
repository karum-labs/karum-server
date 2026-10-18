package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/karum-labs/karum-server/internal/places"
	"github.com/karum-labs/karum-server/internal/routes"
)

type countingSeeds struct {
	calls int
}

func (c *countingSeeds) ResolveBrandSeed(context.Context, SeedQuery) (SeedResolution, error) {
	c.calls++
	return SeedResolution{Entities: []SeedEntity{{ID: "seed-raw", Type: "urn:entity:brand", Name: "Sola"}}}, nil
}

type countingHeatmap struct{ calls int }

func (c *countingHeatmap) GetLocalityHeatmap(context.Context, HeatmapQuery) (HeatmapView, error) {
	c.calls++
	return HeatmapView{Available: true, CellCount: 12, Corridors: []string{"East Austin"}}, nil
}

type countingCompare struct{ calls int }

func (c *countingCompare) CompareAudiences(context.Context, CompareQuery) (CompareChart, error) {
	c.calls++
	return CompareChart{Available: true, Tags: []Tag{{URN: "urn:tag:genre:place:cafe", Label: "Cafe"}}}, nil
}

type closingPlaces struct{}

func (closingPlaces) Enrich(_ context.Context, targets []places.Target) ([]places.Enrichment, error) {
	out := make([]places.Enrichment, len(targets))
	for i, target := range targets {
		out[i] = places.Enrichment{
			ID: target.ID, Address: target.Name + " address", Website: "https://" + target.ID + ".example",
			OpenStatus: "operational", Hours: "Monday: 9-5", Lat: 30.26, Lng: -97.74,
			Attribution: places.Attribution,
		}
		if target.ID == "d-2" {
			out[i].Closed = true
			out[i].OpenStatus = "closed_permanently"
		}
	}
	return out, nil
}

type failingPlaces struct{}

func (failingPlaces) Enrich(_ context.Context, targets []places.Target) ([]places.Enrichment, error) {
	out := make([]places.Enrichment, len(targets))
	for i, target := range targets {
		out[i] = places.Enrichment{ID: target.ID, OpenStatus: "unknown", Unknown: true, Attribution: places.Attribution}
	}
	return out, errors.New("places down")
}

type failingRoutes struct{}

func (failingRoutes) Plan(context.Context, string, []routes.Stop) (routes.Plan, error) {
	return routes.Plan{Failed: true, Error: "routes down", Attribution: routes.Attribution}, nil
}

type okRoutes struct{}

func (okRoutes) Plan(_ context.Context, mode string, stops []routes.Stop) (routes.Plan, error) {
	if mode == string(ModeFlagship) || len(stops) < 2 {
		return routes.Plan{Skipped: true, SkipReason: "not a field route"}, nil
	}
	return routes.Plan{
		Polyline: "encoded", Attribution: routes.Attribution,
		StopIDs: []string{stops[1].ID, stops[0].ID},
		Legs:    []routes.Leg{{FromID: stops[1].ID, ToID: stops[0].ID, Duration: "8 min", DistanceMeters: 900, Polyline: "leg"}},
	}, nil
}

type blockingTaste struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockingTaste) ScoutDoors(ctx context.Context, _ ScoutRequest) (ScoutResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return ScoutResult{}, ctx.Err()
}

type deadlineTaste struct {
	ok bool
}

func (d *deadlineTaste) ScoutDoors(ctx context.Context, _ ScoutRequest) (ScoutResult, error) {
	_, d.ok = ctx.Deadline()
	return healthyTaste().result, nil
}

func TestConfirmedSeedSkipsResolution(t *testing.T) {
	seeds := &countingSeeds{}
	runner := NewRunner(healthyTaste(), healthyAgent(), nil).WithSeedResolver(seeds)
	if _, err := runner.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatal(err)
	}
	if seeds.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", seeds.calls)
	}
}

func TestRawQueryResolvesBeforeScout(t *testing.T) {
	seeds := &countingSeeds{}
	taste := healthyTaste()
	runner := NewRunner(taste, healthyAgent(), nil).WithSeedResolver(seeds)
	req := testRequest(false)
	req.Seed = nil
	req.RawBrandQuery = "Sola"
	run, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if seeds.calls != 1 {
		t.Fatalf("resolver calls = %d", seeds.calls)
	}
	if taste.calls[0].Seed[0].ID != "seed-raw" {
		t.Fatalf("scout seed = %+v", taste.calls[0].Seed)
	}
	if run.State != StateReady && run.State != StateDegraded {
		t.Fatalf("state = %s", run.State)
	}
}

func TestFlagshipHeatmapAndCompareOnlyForIncumbents(t *testing.T) {
	heat := &countingHeatmap{}
	compare := &countingCompare{}
	agent := healthyAgent()
	agent.auditResult.Findings[0].Incumbents = []Incumbent{{Name: "Lilia"}}
	agent.auditResult.Findings[0].CompetitorCount = 1
	runner := NewRunner(healthyTaste(), agent, nil).WithHeatmap(heat).WithCompare(compare)
	req := testRequest(false)
	req.Mode = ModeFlagship
	req.HeatmapRadius = 10000
	run, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if heat.calls != 1 {
		t.Fatalf("heatmap calls = %d", heat.calls)
	}
	if compare.calls != 1 {
		t.Fatalf("compare calls = %d, want the one incumbent door", compare.calls)
	}
	if run.Heatmap == nil || !run.Heatmap.Available {
		t.Fatalf("heatmap = %+v", run.Heatmap)
	}
	wholesale := NewRunner(healthyTaste(), healthyAgent(), nil).WithHeatmap(heat)
	before := heat.calls
	if _, err := wholesale.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatal(err)
	}
	if heat.calls != before {
		t.Fatal("wholesale called the heatmap")
	}
}

func TestSaturationAndFloorFreezeTheBoard(t *testing.T) {
	taste := healthyTaste()
	taste.result.AffinityFloor = 0.5
	taste.result.Doors[1].Affinity.RawValue = 0.2
	taste.result.Doors[1].Affinity.Value = 0.2
	agent := healthyAgent()
	agent.auditResult.Findings[0].CompetitorCount = 4
	runner := NewRunner(taste, agent, nil)
	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Doors) != 0 {
		t.Fatalf("doors = %+v, want both skipped", run.Doors)
	}
	reasons := map[string]string{}
	for _, item := range run.Skip {
		reasons[item.Entity.ID] = item.Reason
	}
	if reasons["d-1"] != "competitor_saturation" || reasons["d-2"] != "low_affinity" {
		t.Fatalf("skip reasons = %+v", reasons)
	}
	if run.Formula == "" || run.QlooPayload == nil || !run.BaselineStored {
		t.Fatalf("formula/payload/baseline not stored: %+v", run)
	}
}

func TestPlacesClosedSkipsAndUnknownStays(t *testing.T) {
	agent := healthyAgent()
	runner := NewRunner(healthyTaste(), agent, nil).WithPlaces(closingPlaces{})
	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Doors) != 1 || run.Doors[0].Entity.ID != "d-1" {
		t.Fatalf("doors = %+v", run.Doors)
	}
	if run.Doors[0].Enrichment == nil || run.Doors[0].Enrichment.Website == "" {
		t.Fatalf("enrichment = %+v", run.Doors[0].Enrichment)
	}
	var closed bool
	for _, item := range run.Skip {
		if item.Entity.ID == "d-2" && item.Reason == "closed" {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("skip = %+v", run.Skip)
	}
	if len(agent.auditCalls) == 0 || agent.auditCalls[0].Targets[0].Website == "" {
		t.Fatalf("audit website missing: %+v", agent.auditCalls)
	}

	unknown := NewRunner(healthyTaste(), healthyAgent(), nil).WithPlaces(failingPlaces{})
	kept, err := unknown.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	if kept.State != StateReady && kept.State != StateDegraded {
		t.Fatalf("state = %s", kept.State)
	}
	if len(kept.Doors) != 2 || !kept.Doors[0].Enrichment.Unknown {
		t.Fatalf("unknown enrichment did not keep the board: %+v", kept.Doors)
	}
}

func TestRouteFailureKeepsTheCampaign(t *testing.T) {
	taste := healthyTaste()
	taste.result.Doors[0].Lat, taste.result.Doors[0].Lng = 30.26, -97.73
	taste.result.Doors[1].Lat, taste.result.Doors[1].Lng = 30.27, -97.74
	runner := NewRunner(taste, healthyAgent(), nil).WithRoutes(failingRoutes{})
	run, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateReady && run.State != StateDegraded {
		t.Fatalf("state = %s", run.State)
	}
	if run.Route == nil || !run.Route.Failed || run.Route.Attribution == "" {
		t.Fatalf("route = %+v", run.Route)
	}
	if len(run.Doors) != 2 {
		t.Fatalf("doors dropped on route failure: %d", len(run.Doors))
	}

	flagship := testRequest(false)
	flagship.Mode = ModeFlagship
	skipped, err := NewRunner(healthyTaste(), healthyAgent(), nil).WithRoutes(okRoutes{}).Run(context.Background(), flagship)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Route == nil || !skipped.Route.Skipped {
		t.Fatalf("flagship route = %+v", skipped.Route)
	}
}

func TestAbortCancelsTheTasteCall(t *testing.T) {
	taste := &blockingTaste{started: make(chan struct{})}
	runner := NewRunner(taste, healthyAgent(), nil).WithDeadlines(2*time.Second, time.Second, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, testRequest(false))
		done <- err
	}()
	select {
	case <-taste.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scout did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("abort returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abort did not cancel the taste call")
	}
}

func TestTasteCallCarriesADeadline(t *testing.T) {
	taste := &deadlineTaste{}
	runner := NewRunner(taste, healthyAgent(), nil).WithDeadlines(time.Second, time.Second, time.Second, time.Second)
	if _, err := runner.Run(context.Background(), testRequest(false)); err != nil {
		t.Fatal(err)
	}
	if !taste.ok {
		t.Fatal("taste context had no deadline")
	}
}

func TestRerunInsertsANewRun(t *testing.T) {
	store := NewStore()
	runner := NewRunner(healthyTaste(), healthyAgent(), store)
	first, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), testRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("re-run reused the run id")
	}
	if got, ok := store.Get(first.ID); !ok || got.Baseline.Text != first.Baseline.Text {
		t.Fatal("previous snapshot was not left intact")
	}
	newest, ok := store.NewestReady("austin-wholesale")
	if !ok || newest.ID != second.ID {
		t.Fatalf("newest = %+v", newest)
	}
}
