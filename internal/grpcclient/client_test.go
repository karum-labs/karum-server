package grpcclient

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/karum-labs/karum-server/internal/orchestrator"
	agentv1 "github.com/karum-labs/karum-server/internal/pb/karum/agent/v1"
	commonv1 "github.com/karum-labs/karum-server/internal/pb/karum/common/v1"
	tastev1 "github.com/karum-labs/karum-server/internal/pb/karum/taste/v1"
)

type tasteFake struct {
	tastev1.UnimplementedTasteServiceServer
	hadDeadline bool
}

func (f *tasteFake) ScoutDoors(ctx context.Context, req *tastev1.ScoutDoorsRequest) (*tastev1.ScoutDoorsResponse, error) {
	_, f.hadDeadline = ctx.Deadline()
	if req.GetSeed().GetEntities()[0].GetId() != "seed-1" {
		return nil, context.Canceled
	}
	return &tastev1.ScoutDoorsResponse{
		Doors: []*tastev1.DoorCandidate{{
			Entity:   &commonv1.EntityRef{Id: "d-1", Type: "urn:entity:place", Name: "Whisler's"},
			Affinity: &commonv1.SubScore{Name: "cultural_affinity", Value: 1, RawValue: 0.91},
			Rank:     1,
		}},
		SkipList: []*tastev1.SkipItem{{
			Entity: &commonv1.EntityRef{Id: "d-9", Name: "Maggie Mae's"},
			Reason: tastev1.SkipReasonCode_SKIP_REASON_CODE_LOW_AFFINITY,
		}},
		Evidence: []*commonv1.EvidenceCall{{Id: "ev-0001", Endpoint: "/v2/insights", ResultCount: 25}},
	}, nil
}

func (f *tasteFake) ResolveBrandSeed(context.Context, *tastev1.ResolveBrandSeedRequest) (*tastev1.ResolveBrandSeedResponse, error) {
	return &tastev1.ResolveBrandSeedResponse{
		Candidates: []*tastev1.SeedCandidate{{
			Entity: &commonv1.EntityRef{Id: "brand-1", Type: "urn:entity:brand", Name: "Sola"},
		}},
	}, nil
}

type agentFake struct {
	agentv1.UnimplementedAgentServiceServer
}

func (agentFake) LlmOnlyBaseline(context.Context, *agentv1.LlmOnlyBaselineRequest) (*agentv1.LlmOnlyBaselineResponse, error) {
	return &agentv1.LlmOnlyBaselineResponse{Text: "ungrounded", VenueNames: []string{"La Condesa"}}, nil
}

func (agentFake) AuditMenu(context.Context, *agentv1.AuditMenuRequest) (*agentv1.AuditMenuResponse, error) {
	return &agentv1.AuditMenuResponse{Results: []*agentv1.MenuAuditResult{{
		Door: &commonv1.EntityRef{Id: "d-1", Name: "Whisler's"}, Succeeded: true, ShelfOpportunity: 0.75,
	}}}, nil
}

func (agentFake) DraftDoorPitch(context.Context, *agentv1.DraftDoorPitchRequest) (*agentv1.DraftDoorPitchResponse, error) {
	return &agentv1.DraftDoorPitchResponse{
		Script: "Whisler's ranks first (ev-0001).", RecommendedSku: &agentv1.Sku{Id: "sku", Name: "Espadin"},
		Attribution: &agentv1.ModelAttribution{
			ModelId: "gemini-3.8-flash", PromptVersion: "draft-door-pitch@v1", EvidenceIds: []string{"ev-0001"},
		},
	}, nil
}

// recordingAgent captures the requests the gateway sends.
type recordingAgent struct {
	agentv1.UnimplementedAgentServiceServer
	draft *agentv1.DraftDoorPitchRequest
	audit *agentv1.AuditMenuRequest
}

func (a *recordingAgent) LlmOnlyBaseline(context.Context, *agentv1.LlmOnlyBaselineRequest) (*agentv1.LlmOnlyBaselineResponse, error) {
	return &agentv1.LlmOnlyBaselineResponse{Text: "ungrounded"}, nil
}

func (a *recordingAgent) AuditMenu(_ context.Context, req *agentv1.AuditMenuRequest) (*agentv1.AuditMenuResponse, error) {
	a.audit = req
	return &agentv1.AuditMenuResponse{}, nil
}

func (a *recordingAgent) DraftDoorPitch(_ context.Context, req *agentv1.DraftDoorPitchRequest) (*agentv1.DraftDoorPitchResponse, error) {
	a.draft = req
	return &agentv1.DraftDoorPitchResponse{Script: "ok"}, nil
}

func TestClientRoundTripsScoutAndHonorsTheDeadline(t *testing.T) {
	tasteLis := listen(t)
	aiLis := listen(t)
	tasteSrv := &tasteFake{}
	start(t, tasteLis, func(s *grpc.Server) { tastev1.RegisterTasteServiceServer(s, tasteSrv) })
	start(t, aiLis, func(s *grpc.Server) { agentv1.RegisterAgentServiceServer(s, agentFake{}) })

	clients, err := Dial(tasteLis.Addr().String(), aiLis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	scout, err := clients.ScoutDoors(ctx, orchestrator.ScoutRequest{
		Seed:         []orchestrator.SeedEntity{{ID: "seed-1", Type: "urn:entity:brand", Name: "Sola"}},
		Point:        orchestrator.Point{Lat: 30.26, Lng: -97.74},
		RadiusMeters: 8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scout.Doors) != 1 || scout.Doors[0].Entity.Name != "Whisler's" || scout.Skip[0].Reason != "low_affinity" {
		t.Fatalf("scout = %+v", scout)
	}
	if !tasteSrv.hadDeadline {
		t.Fatal("the RPC context had no deadline")
	}

	seed, err := clients.ResolveBrandSeed(context.Background(), orchestrator.SeedQuery{BrandQuery: "Sola"})
	if err != nil || seed.Entities[0].ID != "brand-1" {
		t.Fatalf("seed = %+v err = %v", seed, err)
	}
	base, err := clients.LlmOnlyBaseline(context.Background(), orchestrator.BaselineRequest{City: "Austin"})
	if err != nil || base.Text != "ungrounded" || base.VenueNames[0] != "La Condesa" {
		t.Fatalf("baseline = %+v err = %v", base, err)
	}
}

func TestClientSendsTheEvidenceAllowSetAndTheQlooNames(t *testing.T) {
	tasteLis := listen(t)
	aiLis := listen(t)
	agent := &recordingAgent{}
	start(t, tasteLis, func(s *grpc.Server) { tastev1.RegisterTasteServiceServer(s, &tasteFake{}) })
	start(t, aiLis, func(s *grpc.Server) { agentv1.RegisterAgentServiceServer(s, agent) })

	clients, err := Dial(tasteLis.Addr().String(), aiLis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	ctx := context.Background()
	if _, err := clients.DraftDoorPitch(ctx, orchestrator.DraftRequest{
		City:        "Austin",
		EvidenceIDs: []string{"ev-0001", "ev-0002"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := agent.draft.GetEvidenceIds(); len(got) != 2 || got[0] != "ev-0001" || got[1] != "ev-0002" {
		t.Fatalf("the allow-set did not cross the boundary: %v", got)
	}

	if _, err := clients.AuditMenu(ctx, orchestrator.AuditRequest{
		Targets: []orchestrator.AuditTarget{{
			Door:               orchestrator.SeedEntity{ID: "d-1", Name: "Whisler's"},
			QlooIncumbentNames: []string{"Aesop", "Ilegal Mezcal"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	targets := agent.audit.GetTargets()
	if len(targets) != 1 {
		t.Fatalf("targets = %d", len(targets))
	}
	names := targets[0].GetQlooIncumbentNames()
	if len(names) != 2 || names[0] != "Aesop" || names[1] != "Ilegal Mezcal" {
		t.Fatalf("the Qloo names did not cross the boundary: %v", names)
	}
}

func TestClientReadsTheDraftAttribution(t *testing.T) {
	tasteLis := listen(t)
	aiLis := listen(t)
	start(t, tasteLis, func(s *grpc.Server) { tastev1.RegisterTasteServiceServer(s, &tasteFake{}) })
	start(t, aiLis, func(s *grpc.Server) { agentv1.RegisterAgentServiceServer(s, agentFake{}) })

	clients, err := Dial(tasteLis.Addr().String(), aiLis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	draft, err := clients.DraftDoorPitch(context.Background(), orchestrator.DraftRequest{City: "Austin"})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ModelID != "gemini-3.8-flash" || draft.PromptVersion != "draft-door-pitch@v1" {
		t.Fatalf("attribution = %+v", draft)
	}
	if len(draft.EvidenceIDs) != 1 || draft.EvidenceIDs[0] != "ev-0001" {
		t.Fatalf("cited evidence ids = %v", draft.EvidenceIDs)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return lis
}

func start(t *testing.T, lis net.Listener, register func(*grpc.Server)) {
	t.Helper()
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}
