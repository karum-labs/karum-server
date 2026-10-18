package orchestrator

import "time"

// Mode is the campaign mode, matching karum.agent.v1.CampaignMode.
type Mode string

const (
	// ModeWholesale is the door-by-door sales route.
	ModeWholesale Mode = "wholesale"
	// ModeFlagship is the corridor and heatmap read.
	ModeFlagship Mode = "flagship"
	// ModeCollab is the non-competing partner board.
	ModeCollab Mode = "collab"
)

// Point is one lat/lng pair reused by the map, the scout radius, and Qloo.
type Point struct {
	Lat float64
	Lng float64
}

// SeedEntity is one confirmed seed row. Ids are resolved by the seed builder
// before a run starts, so the runner never confirms interactively.
type SeedEntity struct {
	ID   string
	Type string
	Name string
	City string
}

// Tag is one verified tag URN.
type Tag struct {
	URN   string
	Label string
}

// SubScore is one 0–1 term of the door composite, with its inputs beside it.
type SubScore struct {
	Name     string
	Value    float64
	RawValue float64
	Inputs   []string
}

// Door is one recommended door from service-taste.
type Door struct {
	Entity               SeedEntity
	Rank                 int
	Affinity             SubScore
	ShelfOpportunity     *SubScore
	DemographicAlignment *SubScore
	SharedTags           []Tag
	EvidenceIDs          []string
}

// SkipItem is one candidate below the returned shortlist floor.
type SkipItem struct {
	Entity      SeedEntity
	Reason      string
	RawAffinity float64
	Floor       float64
}

// EvidenceCall is one recorded Qloo call the evidence drawer lists.
type EvidenceCall struct {
	ID          string
	Endpoint    string
	Parameters  []string
	ResultCount int
}

// ScoutRequest is the confirmed seed, point, radius, and skip candidates sent
// to service-taste.
type ScoutRequest struct {
	Seed               []SeedEntity
	Point              Point
	RadiusMeters       int
	CategoryTags       []string
	SkipCandidateIDs   []string
	SkipCandidateNames []string
	Mode               Mode
}

// ScoutResult is the door board plus the skip list and evidence bundle.
type ScoutResult struct {
	Doors        []Door
	Skip         []SkipItem
	Evidence     []EvidenceCall
	Warnings     []string
	ErrorCode    string
	ErrorMessage string
}

// AuditTarget is one door sent to the menu audit.
type AuditTarget struct {
	Door          SeedEntity
	Website       string
	Tags          []Tag
	SkipCandidate bool
}

// Source is one grounding citation. It is never a fetch target.
type Source struct {
	URL   string
	Title string
}

// Incumbent is one competitor found on the live menu or shelf.
type Incumbent struct {
	Name    string
	Sources []Source
}

// AuditFinding is one venue's shelf read.
type AuditFinding struct {
	Door             SeedEntity
	Succeeded        bool
	ShelfUnknown     bool
	Incumbents       []Incumbent
	CompetitorCount  int
	ShelfOpportunity float64
	MenuSummary      string
	Sources          []Source
}

// AuditRequest names the working shortlist to audit.
type AuditRequest struct {
	Mode    Mode
	Targets []AuditTarget
}

// AuditResult carries one finding per target, or an upstream error code.
type AuditResult struct {
	Findings     []AuditFinding
	ErrorCode    string
	ErrorMessage string
}

// SKU is one entry of the brand's own product list.
type SKU struct {
	ID   string
	Name string
}

// BrandContext is the brand side of a draft or baseline request.
type BrandContext struct {
	Name string
	SKUs []SKU
}

// Battlecard is the displacement card attached when an incumbent is present.
type Battlecard struct {
	Headline       string
	IncumbentName  string
	TalkingPoints  []string
	CompareSummary string
}

// DraftRequest is one door's pitch request; the Qloo payload is already in it.
type DraftRequest struct {
	Brand       BrandContext
	Door        Door
	Finding     AuditFinding
	City        string
	Mode        Mode
	EvidenceIDs []string
}

// DraftResult is the script, battlecard, SKU, and mode paragraph.
type DraftResult struct {
	Script         string
	Battlecard     *Battlecard
	RecommendedSKU SKU
	ModeParagraph  string
	ErrorCode      string
	ErrorMessage   string
}

// BaselineRequest is the generic-LLM counterfactual for the same inputs.
type BaselineRequest struct {
	Brand BrandContext
	City  string
	Mode  Mode
}

// BaselineResult is stored beside the Qloo result and labeled ungrounded.
type BaselineResult struct {
	Text         string
	VenueNames   []string
	ErrorCode    string
	ErrorMessage string
}

// Pitch is the deliverable stored on a run for one door.
type Pitch struct {
	Script         string
	Battlecard     *Battlecard
	RecommendedSKU SKU
	ModeParagraph  string
	Template       bool
}

// TraceEvent is one completed hop the client timeline renders.
type TraceEvent struct {
	Stage     Stage
	ElapsedMS int64
	CacheHit  bool
	Detail    string
}

// Run is one scout campaign. A ready or degraded run freezes its evidence.
type Run struct {
	ID             string
	PresetID       string
	Mode           Mode
	FreeEntry      bool
	Brand          BrandContext
	City           string
	State          JobState
	Doors          []Door
	Skip           []SkipItem
	Evidence       []EvidenceCall
	Findings       map[string]AuditFinding
	Baseline       BaselineResult
	BaselineStored bool
	Pitches        map[string]Pitch
	Warnings       []string
	Trace          []TraceEvent
	CreatedAt      time.Time
}
