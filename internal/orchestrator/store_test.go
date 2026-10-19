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
	store.Put(&Run{ID: "run-0010", PresetID: "brooklyn-flagship", State: StateDegraded, CreatedAt: sameTick})

	got, ok := store.NewestReady("brooklyn-flagship")
	if !ok {
		t.Fatal("NewestReady() found no run")
	}
	if got.ID != "run-0010" {
		t.Fatalf("NewestReady() = %s, want run-0010", got.ID)
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
