package orchestrator

import (
	"encoding/json"
	"time"
)

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
	ID    string
	Type  string
	Name  string
	City  string
	Place *PlaceFacts
}

// PlaceFacts is the recorded Qloo place block carried beside a venue row
// (PLAN.md §34). It fills the card when the Places read is unavailable.
type PlaceFacts struct {
	Address  string
	Website  string
	Phone    string
	Hours    string
	IsClosed bool
	Lat      float64
	Lng      float64
}

// HasLocation reports whether the recorded place block carries a usable point.
func (p *PlaceFacts) HasLocation() bool {
	return p != nil && (p.Lat != 0 || p.Lng != 0)
}

// Tag is one verified tag URN.
type Tag struct {
	URN   string
	Label string
}

// SubScore is one 0–1 term of the door composite, with its inputs beside it.
// Source names where the raw value came from: qloo_fact, model_inference,
// google_fact, or similar. It surfaces the proto provenance marker on the wire
// so the client and PDF can style facts and inferences distinctly (X-07).
type SubScore struct {
	Name     string
	Value    float64
	RawValue float64
	Inputs   []string
	Source   string
}

// Explainability is one seed input's contribution to a door's affinity,
// straight from Qloo's explainability output.
type Explainability struct {
	EntityID string
	Label    string
	Score    float64
}

// DemographicBucket is one age bucket's delta versus the population baseline.
type DemographicBucket struct {
	Label string
	Delta float64
}

// Demographics is one door's demographic alignment inputs: the age buckets
// and the male and female deltas.
type Demographics struct {
	AgeBuckets  []DemographicBucket
	MaleDelta   float64
	FemaleDelta float64
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
	// Explainability is the per-input contribution list behind the affinity.
	Explainability []Explainability
	// Demographics is the door's demographic profile, when Qloo reported one.
	Demographics *Demographics
	// DistanceMeters is the straight-line distance from the preset point.
	DistanceMeters float64
	// Composite is S_door, assembled by the gateway. Value is 0–1 and
	// RawValue is the 0–100 display number.
	Composite *SubScore
	// Enrichment is the Places read. Unknown stays true when Places was
	// skipped or failed; the door remains on the board.
	Enrichment *Enrichment
	// Compare is the conquest chart, set only after an incumbent is found.
	Compare     *CompareChart
	Lat         float64
	Lng         float64
	OutsideCity bool
}

// SkipItem is one candidate below the returned shortlist floor.
type SkipItem struct {
	Entity          SeedEntity
	Reason          string
	ReasonDetail    string
	RawAffinity     float64
	Floor           float64
	CompetitorCount int
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
	Doors         []Door
	Skip          []SkipItem
	Evidence      []EvidenceCall
	Warnings      []string
	ErrorCode     string
	ErrorMessage  string
	AffinityFloor float64
}

// AuditTarget is one door sent to the menu audit.
type AuditTarget struct {
	Door          SeedEntity
	Website       string
	Tags          []Tag
	SkipCandidate bool
	// QlooIncumbentNames are the competitor names the Qloo payload already
	// carries for this campaign. A competitor the model names survives only
	// when a grounding source or one of these names carries it (PLAN.md §16).
	QlooIncumbentNames []string
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

// Citation is one machine-readable citation: an evidence id the draft cites
// plus the citation text the model wrote. Prose never carries ids inline;
// citations render as a separate source list at the bottom of the pitch
// (PLAN.md §64).
type Citation struct {
	EvidenceID string
	Text       string
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
	// Instructions is the operator's direction for the redraft (PLAN.md §65):
	// a suggestion, not a rule. It may include extracted attachment text, both
	// fenced as untrusted data before reaching the model.
	Instructions string
	// AttachmentID and AttachmentName identify the uploaded brand file, when
	// one was attached. They are stored with the pitch so the client can show
	// a chip and the run records which brand material informed the draft.
	AttachmentID   string
	AttachmentName string
	// MaxWords is the hard cap per text the service enforces
	// (agent.proto: the cap so a judge can finish the read).
	MaxWords int
}

// DraftResult is the script, battlecard, SKU, and mode paragraph.
type DraftResult struct {
	Script         string
	Battlecard     *Battlecard
	RecommendedSKU SKU
	ModeParagraph  string
	// Opening, WhyThisDoor, Pitch, ProductToLead, Ask, Close are the six
	// citation-free sections of the pitch (PLAN.md §64).
	Opening       string
	WhyThisDoor   string
	Pitch         string
	ProductToLead string
	Ask           string
	Close         string
	// Sources are the machine-readable citations referenced by the prose.
	Sources []Citation
	// ModelID, PromptVersion, and EvidenceIDs are the AI response's
	// attribution; every response carries it (PLAN.md §3).
	ModelID       string
	PromptVersion string
	EvidenceIDs   []string
	ErrorCode     string
	ErrorMessage  string
}

// BaselineRequest is the generic-LLM counterfactual for the same inputs.
type BaselineRequest struct {
	Brand BrandContext
	City  string
	Mode  Mode
	// MaxWords is the hard cap for the baseline text the service enforces.
	MaxWords int
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
	// Opening, WhyThisDoor, Pitch, ProductToLead, Ask, Close are the six
	// citation-free sections of the pitch (PLAN.md §64).
	Opening       string
	WhyThisDoor   string
	Pitch         string
	ProductToLead string
	Ask           string
	Close         string
	// Sources are the machine-readable citations referenced by the prose.
	Sources []Citation
	// AttachmentID and AttachmentName identify the brand file attached when
	// the pitch was generated or regenerated (PLAN.md §65).
	AttachmentID   string
	AttachmentName string
	// ModelID, PromptVersion, and EvidenceIDs are the AI response's
	// attribution, kept with the pitch so the run records which model wrote
	// it and which evidence it cited (PLAN.md §3).
	ModelID       string
	PromptVersion string
	EvidenceIDs   []string
}

// TraceEvent is one completed hop the client timeline renders.
type TraceEvent struct {
	Sequence  int64
	Cursor    string
	Stage     Stage
	ElapsedMS int64
	CacheHit  bool
	Detail    string
}

// Run is one scout campaign. A ready or degraded run freezes its evidence.
type Run struct {
	ID               string
	PresetID         string
	Mode             Mode
	FreeEntry        bool
	Brand            BrandContext
	City             string
	Seed             []SeedEntity
	State            JobState
	Doors            []Door
	Skip             []SkipItem
	Evidence         []EvidenceCall
	Findings         map[string]AuditFinding
	Baseline         BaselineResult
	BaselineStored   bool
	Pitches          map[string]Pitch
	Warnings         []string
	Trace            []TraceEvent
	CreatedAt        time.Time
	CampaignID       string
	Point            Point
	RadiusMeters     int
	PointAttribution string
	FormattedAddress string
	AccountID        string
	Heatmap          *HeatmapView
	Route            *RouteView
	QlooPayload      json.RawMessage
	GroundingPayload json.RawMessage
	Formula          string
	TraceID          string
	ErrorCode        string
	ErrorMessage     string
	AffinityFloor    float64
}

// Enrichment is the Places read attached to one door.
type Enrichment struct {
	ID          string
	Address     string
	Hours       string
	Website     string
	Phone       string
	OpenStatus  string
	Lat         float64
	Lng         float64
	Closed      bool
	Unknown     bool
	Attribution string
}

// CompareChart is the conquest overlap for one door.
type CompareChart struct {
	Available            bool
	Reason               string
	Tags                 []Tag
	MatchEntitiesPresent bool
}

// HeatmapCell is one density point returned by service-taste. Latitude and
// longitude are the cell centroid; affinity is a 0–1 Qloo value.
type HeatmapCell struct {
	Lat          float64
	Lng          float64
	Affinity     float64
	AffinityRank int
	Popularity   float64
	Geohash      string
}

// HeatmapView is the flagship density layer the map draws.
type HeatmapView struct {
	Available  bool
	Reason     string
	Corridors  []string
	Anchors    []SeedEntity
	Cells      []HeatmapCell
	CellCount  int
	Localities []LocalityPolygon
}

// LocalityPolygon is one locality boundary the map fills behind the doors.
type LocalityPolygon struct {
	Name     string
	Affinity float64
	Centroid *Point
	Polygon  []Point
}

// RouteView is the field route, or the reason it was not built.
type RouteView struct {
	Skipped     bool
	SkipReason  string
	Failed      bool
	Error       string
	Polyline    string
	Legs        []RouteLeg
	StopIDs     []string
	Attribution string
}

// RouteLeg is one hop of the field route.
type RouteLeg struct {
	FromID         string
	ToID           string
	Duration       string
	Polyline       string
	DistanceMeters int
}

// CampaignRecord is the durable campaign header a refresh restores.
type CampaignRecord struct {
	ID           string
	PresetID     string
	Mode         Mode
	City         string
	Point        Point
	RadiusMeters int
	BrandName    string
	Seed         []SeedEntity
	AccountID    string
	CreatedAt    time.Time
}

// SeedQuery is a raw brand query resolved only when the request has no
// confirmed entities.
type SeedQuery struct {
	BrandQuery string
	Fallbacks  []string
	Point      Point
	City       string
}

// SeedResolution is the resolver's answer.
type SeedResolution struct {
	Entities     []SeedEntity
	Unresolved   bool
	ErrorCode    string
	ErrorMessage string
}

// HeatmapQuery is the flagship heatmap request.
type HeatmapQuery struct {
	Seed         []SeedEntity
	Point        Point
	RadiusMeters int
}

// CompareQuery is one door against the emerging brand.
type CompareQuery struct {
	DoorID        string
	BrandID       string
	IncumbentName string
}
