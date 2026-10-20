// Package grpcclient is the gateway's dial to service-taste and service-ai.
//
// Every call uses the context the runner already bounded with a deadline, so
// a client abort cancels the RPC. Business misses stay on the response as an
// error code; only a transport failure becomes an UpstreamError.
package grpcclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/karum-labs/karum-server/internal/orchestrator"
	agentv1 "github.com/karum-labs/karum-server/internal/pb/karum/agent/v1"
	commonv1 "github.com/karum-labs/karum-server/internal/pb/karum/common/v1"
	tastev1 "github.com/karum-labs/karum-server/internal/pb/karum/taste/v1"
)

// Clients dials both services. One value satisfies the taste, heatmap,
// compare, seed, and agent interfaces the runner calls.
type Clients struct {
	taste tastev1.TasteServiceClient
	agent agentv1.AgentServiceClient
	conns []*grpc.ClientConn
}

// Dial opens insecure connections on the compose network. TLS terminates at
// the public proxy, not between these processes.
func Dial(tasteAddr, aiAddr string) (*Clients, error) {
	// Heatmap responses for a flagship run carry thousands of cells and can
	// exceed the 4 MiB gRPC default; the map needs the full recorded layer.
	maxReceive := grpc.MaxCallRecvMsgSize(16 << 20)
	tasteConn, err := grpc.NewClient(tasteAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(maxReceive),
	)
	if err != nil {
		return nil, fmt.Errorf("dial taste: %w", err)
	}
	aiConn, err := grpc.NewClient(aiAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(maxReceive),
	)
	if err != nil {
		_ = tasteConn.Close()
		return nil, fmt.Errorf("dial ai: %w", err)
	}
	return &Clients{
		taste: tastev1.NewTasteServiceClient(tasteConn),
		agent: agentv1.NewAgentServiceClient(aiConn),
		conns: []*grpc.ClientConn{tasteConn, aiConn},
	}, nil
}

// Close releases both connections.
func (c *Clients) Close() error {
	if c == nil {
		return nil
	}
	var first error
	for _, conn := range c.conns {
		if err := conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ResolveBrandSeed implements orchestrator.SeedResolver.
func (c *Clients) ResolveBrandSeed(ctx context.Context, req orchestrator.SeedQuery) (orchestrator.SeedResolution, error) {
	resp, err := c.taste.ResolveBrandSeed(ctx, &tastev1.ResolveBrandSeedRequest{
		BrandQuery:      req.BrandQuery,
		FallbackQueries: req.Fallbacks,
		Location:        point(req.Point),
		City:            req.City,
	})
	if err != nil {
		return orchestrator.SeedResolution{}, rpcErr(err)
	}
	out := orchestrator.SeedResolution{
		Unresolved:   resp.GetUnresolved(),
		ErrorCode:    codeName(resp.GetErrorCode()),
		ErrorMessage: resp.GetErrorMessage(),
	}
	for _, cand := range resp.GetCandidates() {
		out.Entities = append(out.Entities, fromEntity(cand.GetEntity()))
	}
	return out, nil
}

// ScoutDoors implements orchestrator.TasteClient.
func (c *Clients) ScoutDoors(ctx context.Context, req orchestrator.ScoutRequest) (orchestrator.ScoutResult, error) {
	resp, err := c.taste.ScoutDoors(ctx, &tastev1.ScoutDoorsRequest{
		Seed: &tastev1.BrandSeed{
			Entities: entities(req.Seed),
		},
		Location:     point(req.Point),
		RadiusMeters: int32(req.RadiusMeters),
		CategoryTags: urns(req.CategoryTags),
	})
	if err != nil {
		return orchestrator.ScoutResult{}, rpcErr(err)
	}
	out := orchestrator.ScoutResult{
		Warnings:     resp.GetWarnings(),
		ErrorCode:    codeName(resp.GetErrorCode()),
		ErrorMessage: resp.GetErrorMessage(),
		Evidence:     evidence(resp.GetEvidence()),
	}
	for _, door := range resp.GetDoors() {
		entity := fromEntity(door.GetEntity())
		lat, lng := 0.0, 0.0
		if entity.Place.HasLocation() {
			lat, lng = entity.Place.Lat, entity.Place.Lng
		}
		out.Doors = append(out.Doors, orchestrator.Door{
			Entity:               entity,
			Rank:                 int(door.GetRank()),
			Affinity:             fromScore(door.GetAffinity()),
			DemographicAlignment: fromScorePtr(door.GetDemographicAlignment()),
			SharedTags:           fromTags(door.GetSharedTags()),
			EvidenceIDs:          append([]string(nil), door.GetEvidenceIds()...),
			OutsideCity:          door.GetOutsideCity(),
			Lat:                  lat,
			Lng:                  lng,
		})
	}
	for _, item := range resp.GetSkipList() {
		out.Skip = append(out.Skip, orchestrator.SkipItem{
			Entity:          fromEntity(item.GetEntity()),
			Reason:          skipReason(item.GetReason()),
			RawAffinity:     item.GetAffinity().GetRawValue(),
			CompetitorCount: int(item.GetCompetitorCount()),
		})
	}
	return out, nil
}

// GetLocalityHeatmap implements orchestrator.HeatmapClient.
func (c *Clients) GetLocalityHeatmap(ctx context.Context, req orchestrator.HeatmapQuery) (orchestrator.HeatmapView, error) {
	resp, err := c.taste.GetLocalityHeatmap(ctx, &tastev1.GetLocalityHeatmapRequest{
		Entities:          entities(req.Seed),
		Location:          point(req.Point),
		RadiusMeters:      int32(req.RadiusMeters),
		IncludeLocalities: true,
	})
	if err != nil {
		return orchestrator.HeatmapView{}, rpcErr(err)
	}
	view := orchestrator.HeatmapView{
		Available: resp.GetAvailable(),
		Reason:    firstNonEmpty(resp.GetUnavailableReason(), resp.GetErrorMessage()),
		Corridors: append([]string(nil), resp.GetCorridors()...),
		CellCount: len(resp.GetCells()),
	}
	for _, anchor := range resp.GetAnchors() {
		view.Anchors = append(view.Anchors, fromEntity(anchor))
	}
	for _, cell := range resp.GetCells() {
		loc := cell.GetLocation()
		view.Cells = append(view.Cells, orchestrator.HeatmapCell{
			Lat:          loc.GetLatitude(),
			Lng:          loc.GetLongitude(),
			Affinity:     cell.GetAffinity(),
			AffinityRank: int(cell.GetAffinityRank()),
			Popularity:   cell.GetPopularity(),
			Geohash:      cell.GetGeohash(),
		})
	}
	for _, locality := range resp.GetLocalities() {
		item := orchestrator.LocalityPolygon{
			Name:     locality.GetName(),
			Affinity: locality.GetAffinity(),
		}
		if centroid := locality.GetCentroid(); centroid != nil {
			item.Centroid = &orchestrator.Point{Lat: centroid.GetLatitude(), Lng: centroid.GetLongitude()}
		}
		for _, vertex := range locality.GetPolygon() {
			item.Polygon = append(item.Polygon, orchestrator.Point{
				Lat: vertex.GetLatitude(), Lng: vertex.GetLongitude(),
			})
		}
		view.Localities = append(view.Localities, item)
	}
	return view, nil
}

// CompareAudiences implements orchestrator.CompareClient.
func (c *Clients) CompareAudiences(ctx context.Context, req orchestrator.CompareQuery) (orchestrator.CompareChart, error) {
	resp, err := c.taste.CompareAudiences(ctx, &tastev1.CompareAudiencesRequest{
		EmergingBrand: &commonv1.EntityRef{Id: req.BrandID, Type: "urn:entity:brand"},
		Venue:         &commonv1.EntityRef{Id: req.DoorID, Type: "urn:entity:place"},
		IncumbentName: req.IncumbentName,
	})
	if err != nil {
		return orchestrator.CompareChart{}, rpcErr(err)
	}
	chart := orchestrator.CompareChart{
		Available:            resp.GetAvailable(),
		Reason:               firstNonEmpty(resp.GetUnavailableReason(), resp.GetErrorMessage()),
		Tags:                 fromTags(resp.GetOverlapTags()),
		MatchEntitiesPresent: resp.GetMatchEntitiesPresent(),
	}
	return chart, nil
}

// AuditMenu implements the audit half of orchestrator.AgentClient.
func (c *Clients) AuditMenu(ctx context.Context, req orchestrator.AuditRequest) (orchestrator.AuditResult, error) {
	targets := make([]*agentv1.MenuAuditTarget, len(req.Targets))
	for i, target := range req.Targets {
		targets[i] = &agentv1.MenuAuditTarget{
			Door:               entity(target.Door),
			Website:            target.Website,
			TasteTags:          tags(target.Tags),
			SkipCandidate:      target.SkipCandidate,
			QlooIncumbentNames: append([]string(nil), target.QlooIncumbentNames...),
		}
	}
	resp, err := c.agent.AuditMenu(ctx, &agentv1.AuditMenuRequest{
		Mode:    campaignMode(req.Mode),
		Targets: targets,
	})
	if err != nil {
		return orchestrator.AuditResult{}, rpcErr(err)
	}
	out := orchestrator.AuditResult{
		ErrorCode:    codeName(resp.GetErrorCode()),
		ErrorMessage: resp.GetErrorMessage(),
	}
	for _, result := range resp.GetResults() {
		finding := orchestrator.AuditFinding{
			Door:             fromEntity(result.GetDoor()),
			Succeeded:        result.GetSucceeded(),
			ShelfUnknown:     result.GetShelfUnknown(),
			CompetitorCount:  int(result.GetCompetitorCount()),
			ShelfOpportunity: result.GetShelfOpportunity(),
			MenuSummary:      result.GetMenuSummary(),
		}
		for _, incumbent := range result.GetIncumbents() {
			item := orchestrator.Incumbent{Name: incumbent.GetName()}
			for _, source := range incumbent.GetSources() {
				item.Sources = append(item.Sources, orchestrator.Source{URL: source.GetUrl(), Title: source.GetTitle()})
			}
			finding.Incumbents = append(finding.Incumbents, item)
		}
		for _, source := range result.GetSources() {
			finding.Sources = append(finding.Sources, orchestrator.Source{URL: source.GetUrl(), Title: source.GetTitle()})
		}
		out.Findings = append(out.Findings, finding)
	}
	return out, nil
}

// DraftDoorPitch implements the draft half of orchestrator.AgentClient.
func (c *Clients) DraftDoorPitch(ctx context.Context, req orchestrator.DraftRequest) (orchestrator.DraftResult, error) {
	resp, err := c.agent.DraftDoorPitch(ctx, &agentv1.DraftDoorPitchRequest{
		Mode: campaignMode(req.Mode),
		City: req.City,
		Brand: &agentv1.BrandContext{
			Name: req.Brand.Name,
			Skus: skus(req.Brand.SKUs),
		},
		Door: doorContext(req),
		// The run's evidence set is the allow-set a cited id must belong to.
		// An empty set is strict: every sentence that states a number is
		// dropped (PLAN.md §17, §18).
		EvidenceIds: append([]string(nil), req.EvidenceIDs...),
	})
	if err != nil {
		return orchestrator.DraftResult{}, rpcErr(err)
	}
	out := orchestrator.DraftResult{
		Script:         resp.GetScript(),
		ModeParagraph:  resp.GetModeParagraph(),
		RecommendedSKU: orchestrator.SKU{ID: resp.GetRecommendedSku().GetId(), Name: resp.GetRecommendedSku().GetName()},
		ModelID:        resp.GetAttribution().GetModelId(),
		PromptVersion:  resp.GetAttribution().GetPromptVersion(),
		EvidenceIDs:    append([]string(nil), resp.GetAttribution().GetEvidenceIds()...),
		ErrorCode:      codeName(resp.GetErrorCode()),
		ErrorMessage:   resp.GetErrorMessage(),
	}
	if card := resp.GetBattlecard(); card != nil {
		out.Battlecard = &orchestrator.Battlecard{
			Headline:       card.GetHeadline(),
			IncumbentName:  card.GetIncumbentName(),
			TalkingPoints:  append([]string(nil), card.GetTalkingPoints()...),
			CompareSummary: card.GetCompareSummary(),
		}
	}
	return out, nil
}

// LlmOnlyBaseline implements the baseline half of orchestrator.AgentClient.
func (c *Clients) LlmOnlyBaseline(ctx context.Context, req orchestrator.BaselineRequest) (orchestrator.BaselineResult, error) {
	resp, err := c.agent.LlmOnlyBaseline(ctx, &agentv1.LlmOnlyBaselineRequest{
		Brand: &agentv1.BrandContext{Name: req.Brand.Name, Skus: skus(req.Brand.SKUs)},
		City:  req.City,
		Mode:  campaignMode(req.Mode),
	})
	if err != nil {
		return orchestrator.BaselineResult{}, rpcErr(err)
	}
	return orchestrator.BaselineResult{
		Text:         resp.GetText(),
		VenueNames:   append([]string(nil), resp.GetVenueNames()...),
		ErrorCode:    codeName(resp.GetErrorCode()),
		ErrorMessage: resp.GetErrorMessage(),
	}, nil
}

func doorContext(req orchestrator.DraftRequest) *agentv1.DoorContext {
	finding := req.Finding
	audit := &agentv1.MenuAuditResult{
		Door:             entity(finding.Door),
		Succeeded:        finding.Succeeded,
		ShelfUnknown:     finding.ShelfUnknown,
		CompetitorCount:  int32(finding.CompetitorCount),
		ShelfOpportunity: finding.ShelfOpportunity,
		MenuSummary:      finding.MenuSummary,
	}
	for _, incumbent := range finding.Incumbents {
		item := &agentv1.Incumbent{Name: incumbent.Name}
		for _, source := range incumbent.Sources {
			item.Sources = append(item.Sources, &agentv1.GroundingSource{Url: source.URL, Title: source.Title})
		}
		audit.Incumbents = append(audit.Incumbents, item)
	}
	var compare []*commonv1.Tag
	if req.Door.Compare != nil {
		compare = tags(req.Door.Compare.Tags)
	}
	return &agentv1.DoorContext{
		SharedTags:  tags(req.Door.SharedTags),
		Affinity:    toScore(req.Door.Affinity),
		CompareTags: compare,
		Audit:       audit,
	}
}

func rpcErr(err error) error {
	return &orchestrator.UpstreamError{Code: orchestrator.CodeUpstream, Message: err.Error(), Cause: err}
}

func codeName(code commonv1.ErrorCode) string {
	if code == commonv1.ErrorCode_ERROR_CODE_UNSPECIFIED {
		return ""
	}
	return code.String()
}

func skipReason(code tastev1.SkipReasonCode) string {
	switch code {
	case tastev1.SkipReasonCode_SKIP_REASON_CODE_COMPETITOR_SATURATION:
		return "competitor_saturation"
	case tastev1.SkipReasonCode_SKIP_REASON_CODE_CLOSED:
		return "closed"
	case tastev1.SkipReasonCode_SKIP_REASON_CODE_OUTSIDE_CITY:
		return "outside_city"
	case tastev1.SkipReasonCode_SKIP_REASON_CODE_UNRESOLVED:
		return "unresolved"
	default:
		return "low_affinity"
	}
}

func campaignMode(mode orchestrator.Mode) agentv1.CampaignMode {
	switch mode {
	case orchestrator.ModeFlagship:
		return agentv1.CampaignMode_CAMPAIGN_MODE_FLAGSHIP
	case orchestrator.ModeCollab:
		return agentv1.CampaignMode_CAMPAIGN_MODE_COLLAB
	default:
		return agentv1.CampaignMode_CAMPAIGN_MODE_WHOLESALE
	}
}

func point(p orchestrator.Point) *commonv1.GeoPoint {
	return &commonv1.GeoPoint{Latitude: p.Lat, Longitude: p.Lng}
}

func entity(e orchestrator.SeedEntity) *commonv1.EntityRef {
	return &commonv1.EntityRef{Id: e.ID, Type: e.Type, Name: e.Name, City: e.City, Place: placeFacts(e.Place)}
}

func placeFacts(p *orchestrator.PlaceFacts) *commonv1.PlaceFacts {
	if p == nil {
		return nil
	}
	var location *commonv1.GeoPoint
	if p.Lat != 0 || p.Lng != 0 {
		location = &commonv1.GeoPoint{Latitude: p.Lat, Longitude: p.Lng}
	}
	return &commonv1.PlaceFacts{
		Address: p.Address, Website: p.Website, Phone: p.Phone,
		Hours: p.Hours, IsClosed: p.IsClosed, Location: location,
	}
}

func entities(items []orchestrator.SeedEntity) []*commonv1.EntityRef {
	out := make([]*commonv1.EntityRef, len(items))
	for i, item := range items {
		out[i] = entity(item)
	}
	return out
}

func fromEntity(e *commonv1.EntityRef) orchestrator.SeedEntity {
	if e == nil {
		return orchestrator.SeedEntity{}
	}
	return orchestrator.SeedEntity{
		ID: e.GetId(), Type: e.GetType(), Name: e.GetName(), City: e.GetCity(),
		Place: fromPlaceFacts(e.GetPlace()),
	}
}

func fromPlaceFacts(p *commonv1.PlaceFacts) *orchestrator.PlaceFacts {
	if p == nil {
		return nil
	}
	out := &orchestrator.PlaceFacts{
		Address: p.GetAddress(), Website: p.GetWebsite(), Phone: p.GetPhone(),
		Hours: p.GetHours(), IsClosed: p.GetIsClosed(),
	}
	if location := p.GetLocation(); location != nil {
		out.Lat = location.GetLatitude()
		out.Lng = location.GetLongitude()
	}
	return out
}

func urns(values []string) []*commonv1.Tag {
	out := make([]*commonv1.Tag, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, &commonv1.Tag{Urn: value})
		}
	}
	return out
}

func tags(items []orchestrator.Tag) []*commonv1.Tag {
	out := make([]*commonv1.Tag, len(items))
	for i, item := range items {
		out[i] = &commonv1.Tag{Urn: item.URN, Label: item.Label}
	}
	return out
}

func fromTags(items []*commonv1.Tag) []orchestrator.Tag {
	out := make([]orchestrator.Tag, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, orchestrator.Tag{URN: item.GetUrn(), Label: item.GetLabel()})
	}
	return out
}

func toScore(score orchestrator.SubScore) *commonv1.SubScore {
	return &commonv1.SubScore{
		Name: score.Name, Value: score.Value, RawValue: score.RawValue,
		Inputs: append([]string(nil), score.Inputs...),
	}
}

func fromScore(score *commonv1.SubScore) orchestrator.SubScore {
	if score == nil {
		return orchestrator.SubScore{}
	}
	return orchestrator.SubScore{
		Name: score.GetName(), Value: score.GetValue(), RawValue: score.GetRawValue(),
		Inputs: append([]string(nil), score.GetInputs()...),
	}
}

func fromScorePtr(score *commonv1.SubScore) *orchestrator.SubScore {
	if score == nil || score.GetName() == "" && score.GetValue() == 0 && score.GetRawValue() == 0 {
		return nil
	}
	copied := fromScore(score)
	return &copied
}

func skus(items []orchestrator.SKU) []*agentv1.Sku {
	out := make([]*agentv1.Sku, len(items))
	for i, item := range items {
		out[i] = &agentv1.Sku{Id: item.ID, Name: item.Name}
	}
	return out
}

func evidence(items []*commonv1.EvidenceCall) []orchestrator.EvidenceCall {
	out := make([]orchestrator.EvidenceCall, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		call := orchestrator.EvidenceCall{
			ID: item.GetId(), Endpoint: item.GetEndpoint(), ResultCount: int(item.GetResultCount()),
		}
		if item.GetParameters() != "" {
			call.Parameters = []string{item.GetParameters()}
		}
		out = append(out, call)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
