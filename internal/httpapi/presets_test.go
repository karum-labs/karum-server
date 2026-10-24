package httpapi

import (
	"net/http"
	"testing"

	"github.com/karum-labs/karum-server/internal/config"
)

// PLAN.md §52: the four previous-run cards are served from this endpoint instead
// of being hand-duplicated as Dart constants in the client.
func TestGetPresetsServesTheLockedCatalogue(t *testing.T) {
	_, _, srv, _ := testStack(config.ModeFixture)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/presets")
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
	items, ok := body["presets"].([]any)
	if !ok || len(items) != 4 {
		t.Fatalf("presets = %+v", body["presets"])
	}

	austin, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("austin entry = %+v", items[0])
	}
	if austin["id"] != "austin-wholesale" {
		t.Fatalf("first preset id = %v, want austin-wholesale", austin["id"])
	}
	if austin["button_label"] != "Try: Sola Yuzu Mezcal in Austin, TX" {
		t.Fatalf("button_label = %v", austin["button_label"])
	}
	if austin["campaign_id"] != "cmp-austin-wholesale" {
		t.Fatalf("campaign_id = %v", austin["campaign_id"])
	}
	point, ok := austin["point"].(map[string]any)
	if !ok || point["lat"] != 30.2672 {
		t.Fatalf("point = %+v", austin["point"])
	}
	seeds, ok := austin["seeds"].([]any)
	if !ok || len(seeds) != 3 {
		t.Fatalf("seeds = %+v", austin["seeds"])
	}
	skus, ok := austin["skus"].([]any)
	if !ok || len(skus) != 2 {
		t.Fatalf("skus = %+v", austin["skus"])
	}

	// Brooklyn's flagship and collab variants share one group so the client
	// can render them as the two buttons on a single card.
	flagship, ok := items[1].(map[string]any)
	if !ok || flagship["group_id"] != "brooklyn" {
		t.Fatalf("flagship group_id = %+v", items[1])
	}
	collab, ok := items[2].(map[string]any)
	if !ok || collab["group_id"] != "brooklyn" || collab["group_id"] != flagship["group_id"] {
		t.Fatalf("collab group_id = %+v, flagship = %+v", items[2], flagship["group_id"])
	}

	// No auth required: the catalogue is the same for every visitor.
	if resp.Request != nil && resp.Request.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected Authorization header on an anonymous request")
	}
}
