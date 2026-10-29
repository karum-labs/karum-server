package orchestrator

import (
	"testing"
	"time"
)

func TestNewestReadyUsesCreationTime(t *testing.T) {
	store := NewStore()
	earlier := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Second)
	store.Put(&Run{ID: "run-0002", PresetID: "austin-wholesale", State: StateReady, CreatedAt: earlier})
	store.Put(&Run{ID: "run-0001", PresetID: "austin-wholesale", State: StateReady, CreatedAt: later})

	got, ok := store.NewestReady("austin-wholesale")
	if !ok {
		t.Fatal("NewestReady() found no run")
	}
	if got.ID != "run-0001" {
		t.Fatalf("NewestReady() = %s, want the later run-0001", got.ID)
	}
}

func TestNewestReadyBreaksTimestampTiesByNumericRunID(t *testing.T) {
	store := NewStore()
	// Windows clocks can return the same timestamp for two runs started in
	// one tick; the numeric run id must decide, not map iteration order.
	sameTick := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	store.Put(&Run{ID: "run-0009", PresetID: "brooklyn-flagship", State: StateReady, CreatedAt: sameTick})
	store.Put(&Run{ID: "run-0010", PresetID: "brooklyn-flagship", State: StateReady, CreatedAt: sameTick})

	got, ok := store.NewestReady("brooklyn-flagship")
	if !ok {
		t.Fatal("NewestReady() found no run")
	}
	if got.ID != "run-0010" {
		t.Fatalf("NewestReady() = %s, want run-0010", got.ID)
	}
}

func TestNewestReadyPrefersReadyOverANewerDegradedRun(t *testing.T) {
	store := NewStore()
	readyAt := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	degradedAt := readyAt.Add(time.Minute)
	store.Put(&Run{ID: "run-0001", PresetID: "austin-wholesale", State: StateReady, CreatedAt: readyAt})
	store.Put(&Run{ID: "run-0002", PresetID: "austin-wholesale", State: StateDegraded, CreatedAt: degradedAt})

	got, ok := store.NewestReady("austin-wholesale")
	if !ok {
		t.Fatal("NewestReady() found no run")
	}
	if got.ID != "run-0001" {
		t.Fatalf("NewestReady() = %s, want the ready run-0001", got.ID)
	}
}

func TestNewestReadyFallsBackToDegradedWhenNoReadyRunExists(t *testing.T) {
	store := NewStore()
	earlier := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	store.Put(&Run{ID: "run-0001", PresetID: "austin-wholesale", State: StateDegraded, CreatedAt: earlier})
	store.Put(&Run{ID: "run-0002", PresetID: "austin-wholesale", State: StateDegraded, CreatedAt: earlier.Add(time.Second)})

	got, ok := store.NewestReady("austin-wholesale")
	if !ok {
		t.Fatal("NewestReady() found no degraded run")
	}
	if got.ID != "run-0002" {
		t.Fatalf("NewestReady() = %s, want the newer degraded run-0002", got.ID)
	}
}

func TestNewestReadyIgnoresNonTerminalAndFreeEntryRuns(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	store.Put(&Run{ID: "run-0001", PresetID: "mumbai-colaba-wholesale", State: StateRunning, CreatedAt: now})
	store.Put(&Run{ID: "run-0002", PresetID: "mumbai-colaba-wholesale", State: StateReady, CreatedAt: now, FreeEntry: true})

	if got, ok := store.NewestReady("mumbai-colaba-wholesale"); ok {
		t.Fatalf("NewestReady() = %s, want no preset run", got.ID)
	}
}

func TestReservedSequencesContinueAfterHydrate(t *testing.T) {
	store := NewStore()
	store.ReserveRunSeq(12)
	store.ReserveCampaignSeq(3)

	if got := store.NextID(); got != "run-0013" {
		t.Fatalf("NextID() = %s, want run-0013", got)
	}
	if got := store.NextCampaignID(); got != "cmp-0004" {
		t.Fatalf("NextCampaignID() = %s, want cmp-0004", got)
	}

	// A lower reservation must never move a counter backwards.
	store.ReserveRunSeq(2)
	if got := store.NextID(); got != "run-0014" {
		t.Fatalf("NextID() after lower reserve = %s, want run-0014", got)
	}
}
