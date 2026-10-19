package orchestrator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karum-labs/karum-server/internal/presets"
)

// RunStore is the runner's view of run persistence. Postgres implements the
// same methods; tests use the in-memory store.
type RunStore interface {
	NextID() string
	Put(run *Run)
	Get(id string) (*Run, bool)
}

// Store keeps runs for the life of the process. Postgres persistence
// implements the same RunStore surface.
type Store struct {
	mu          sync.RWMutex
	runs        map[string]*Run
	campaigns   map[string]CampaignRecord
	subs        map[string][]chan struct{}
	seq         int
	campaignSeq int
}

// NewStore returns an empty run store seeded with the four preset campaigns
// so fixture and live modes share their ids.
func NewStore() *Store {
	s := &Store{
		runs:      map[string]*Run{},
		campaigns: map[string]CampaignRecord{},
		subs:      map[string][]chan struct{}{},
	}
	for _, preset := range presets.New().All() {
		seed := make([]SeedEntity, len(preset.Seed))
		for i, entity := range preset.Seed {
			seed[i] = SeedEntity{ID: entity.ID, Type: entity.Type, Name: entity.Name}
		}
		s.campaigns[presets.CampaignID(preset.ID)] = CampaignRecord{
			ID:           presets.CampaignID(preset.ID),
			PresetID:     preset.ID,
			Mode:         Mode(preset.Mode),
			City:         preset.City,
			Point:        Point{Lat: preset.Lat, Lng: preset.Lng},
			RadiusMeters: preset.RadiusMeters,
			BrandName:    preset.Brand,
			Seed:         seed,
		}
	}
	return s
}

// NextID returns a stable, human-readable run id.
func (s *Store) NextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("run-%04d", s.seq)
}

// NextCampaignID returns an id for a free-entry campaign.
func (s *Store) NextCampaignID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaignSeq++
	return fmt.Sprintf("cmp-%04d", s.campaignSeq)
}

// Put stores a snapshot of a run. Callers may keep mutating the struct they
// passed in; readers see the snapshot.
func (s *Store) Put(run *Run) {
	if run == nil || run.ID == "" {
		return
	}
	cloned := run.clone()
	s.mu.Lock()
	s.runs[run.ID] = cloned
	subs := append([]chan struct{}(nil), s.subs[run.ID]...)
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Get returns a snapshot of a run by id.
func (s *Store) Get(id string) (*Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	if !ok {
		return nil, false
	}
	return run.clone(), true
}

// Len reports how many runs are stored.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runs)
}

// NewestReady returns the newest ready or degraded run for a preset. Free-entry
// runs are not served on the preset path.
func (s *Store) NewestReady(presetID string) (*Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Run
	for _, run := range s.runs {
		if run.PresetID != presetID || run.FreeEntry {
			continue
		}
		if run.State != StateReady && run.State != StateDegraded {
			continue
		}
		if best == nil || newerRun(run, best) {
			best = run
		}
	}
	if best == nil {
		return nil, false
	}
	return best.clone(), true
}

// newerRun reports whether candidate is newer than best. Creation timestamps
// can tie when two runs start inside one clock tick (a real case on Windows,
// whose system clock is coarser than the nanosecond digits suggest), so the
// monotonically increasing run id breaks the tie. Without it a map-ordered
// scan would return an arbitrary run.
func newerRun(candidate, best *Run) bool {
	if !candidate.CreatedAt.Equal(best.CreatedAt) {
		return candidate.CreatedAt.After(best.CreatedAt)
	}
	return runIDLess(best.ID, candidate.ID)
}

// runIDLess orders run ids by their numeric suffix ("run-0002" after
// "run-0001"), falling back to lexicographic order for ids without one.
func runIDLess(a, b string) bool {
	na, aOK := trailingNumber(a)
	nb, bOK := trailingNumber(b)
	if aOK && bOK {
		return na < nb
	}
	return a < b
}

func trailingNumber(id string) (int, bool) {
	idx := strings.LastIndexByte(id, '-')
	if idx < 0 || idx == len(id)-1 {
		return 0, false
	}
	n, err := strconv.Atoi(id[idx+1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// NewestForCampaign returns the newest run of a campaign, ready or not.
func (s *Store) NewestForCampaign(campaignID string) (*Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Run
	for _, run := range s.runs {
		if run.CampaignID != campaignID {
			continue
		}
		if best == nil || newerRun(run, best) {
			best = run
		}
	}
	if best == nil {
		return nil, false
	}
	return best.clone(), true
}

// UpsertCampaign stores a campaign header without touching its runs.
func (s *Store) UpsertCampaign(record CampaignRecord) {
	if record.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[record.ID] = record
}

// Campaign returns a campaign header by id.
func (s *Store) Campaign(id string) (CampaignRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.campaigns[id]
	if !ok {
		return CampaignRecord{}, false
	}
	record.Seed = append([]SeedEntity(nil), record.Seed...)
	return record, true
}

// Subscribe returns a channel that receives a signal whenever the run is stored,
// and a function that removes the subscription.
func (s *Store) Subscribe(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[id] = append(s.subs[id], ch)
	s.mu.Unlock()
	return ch, func() { s.unsubscribe(id, ch) }
}

func (s *Store) unsubscribe(id string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.subs[id]
	for i, item := range subs {
		if item == ch {
			s.subs[id] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

func (r *Run) clone() *Run {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Doors = cloneDoors(r.Doors)
	cp.Skip = append([]SkipItem(nil), r.Skip...)
	cp.Evidence = cloneEvidence(r.Evidence)
	cp.Warnings = append([]string(nil), r.Warnings...)
	cp.Trace = append([]TraceEvent(nil), r.Trace...)
	cp.Findings = cloneFindings(r.Findings)
	cp.Pitches = clonePitches(r.Pitches)
	cp.QlooPayload = append(json.RawMessage(nil), r.QlooPayload...)
	cp.GroundingPayload = append(json.RawMessage(nil), r.GroundingPayload...)
	cp.Brand.SKUs = append([]SKU(nil), r.Brand.SKUs...)
	cp.Seed = append([]SeedEntity(nil), r.Seed...)
	if r.Heatmap != nil {
		hm := *r.Heatmap
		hm.Corridors = append([]string(nil), r.Heatmap.Corridors...)
		hm.Anchors = append([]SeedEntity(nil), r.Heatmap.Anchors...)
		cp.Heatmap = &hm
	}
	if r.Route != nil {
		rt := *r.Route
		rt.Legs = append([]RouteLeg(nil), r.Route.Legs...)
		rt.StopIDs = append([]string(nil), r.Route.StopIDs...)
		cp.Route = &rt
	}
	return &cp
}

func cloneDoors(doors []Door) []Door {
	out := make([]Door, len(doors))
	for i, door := range doors {
		door.SharedTags = append([]Tag(nil), door.SharedTags...)
		door.EvidenceIDs = append([]string(nil), door.EvidenceIDs...)
		if door.Composite != nil {
			score := *door.Composite
			score.Inputs = append([]string(nil), door.Composite.Inputs...)
			door.Composite = &score
		}
		if door.ShelfOpportunity != nil {
			score := *door.ShelfOpportunity
			score.Inputs = append([]string(nil), door.ShelfOpportunity.Inputs...)
			door.ShelfOpportunity = &score
		}
		if door.DemographicAlignment != nil {
			score := *door.DemographicAlignment
			score.Inputs = append([]string(nil), door.DemographicAlignment.Inputs...)
			door.DemographicAlignment = &score
		}
		if door.Enrichment != nil {
			enr := *door.Enrichment
			door.Enrichment = &enr
		}
		if door.Compare != nil {
			chart := *door.Compare
			chart.Tags = append([]Tag(nil), door.Compare.Tags...)
			door.Compare = &chart
		}
		out[i] = door
	}
	return out
}

func cloneEvidence(calls []EvidenceCall) []EvidenceCall {
	out := make([]EvidenceCall, len(calls))
	for i, call := range calls {
		call.Parameters = append([]string(nil), call.Parameters...)
		out[i] = call
	}
	return out
}

func cloneFindings(in map[string]AuditFinding) map[string]AuditFinding {
	if in == nil {
		return nil
	}
	out := make(map[string]AuditFinding, len(in))
	for key, finding := range in {
		finding.Incumbents = append([]Incumbent(nil), finding.Incumbents...)
		finding.Sources = append([]Source(nil), finding.Sources...)
		out[key] = finding
	}
	return out
}

func clonePitches(in map[string]Pitch) map[string]Pitch {
	if in == nil {
		return nil
	}
	out := make(map[string]Pitch, len(in))
	for key, pitch := range in {
		if pitch.Battlecard != nil {
			card := *pitch.Battlecard
			card.TalkingPoints = append([]string(nil), pitch.Battlecard.TalkingPoints...)
			pitch.Battlecard = &card
		}
		out[key] = pitch
	}
	return out
}

// TouchCreatedAt sets the timestamp used when ordering ready runs. Tests use
// it to prove a re-run does not replace the previous snapshot.
func (s *Store) TouchCreatedAt(id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return
	}
	run.CreatedAt = at
}
