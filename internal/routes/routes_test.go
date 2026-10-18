package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanSkipsFlagshipMissingKeyAndShortSets(t *testing.T) {
	c := Client{Key: "k"}
	for _, tc := range []struct {
		mode  string
		key   string
		stops []Stop
		want  string
	}{
		{mode: "flagship", key: "k", stops: twoStops(), want: "flagship"},
		{mode: "wholesale", key: "", stops: twoStops(), want: "key"},
		{mode: "wholesale", key: "k", stops: []Stop{{ID: "a", Lat: 1, Lng: 1}}, want: "fewer than two"},
	} {
		client := Client{Key: tc.key}
		got, err := client.Plan(context.Background(), tc.mode, tc.stops)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Skipped || !strings.Contains(got.SkipReason, tc.want) {
			t.Fatalf("mode %s key %q plan = %+v", tc.mode, tc.key, got)
		}
	}
	_ = c
}

func TestPlanOrdersIntermediatesAndKeepsAttribution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "routes-key" {
			t.Errorf("key header = %q", r.Header.Get("X-Goog-Api-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"optimizeWaypointOrder":true`) {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{
			"routes":[{
				"polyline":{"encodedPolyline":"overall"},
				"optimizedIntermediateWaypointIndex":[1,0],
				"legs":[
					{"distanceMeters":10,"duration":"60s","polyline":{"encodedPolyline":"l1"}},
					{"distanceMeters":20,"duration":"120s","polyline":{"encodedPolyline":"l2"}},
					{"distanceMeters":30,"duration":"180s","polyline":{"encodedPolyline":"l3"}}
				]
			}]
		}`))
	}))
	defer srv.Close()

	stops := []Stop{
		{ID: "a", Lat: 30.1, Lng: -97.1},
		{ID: "b", Lat: 30.2, Lng: -97.2},
		{ID: "c", Lat: 30.3, Lng: -97.3},
		{ID: "d", Lat: 30.4, Lng: -97.4},
	}
	got, err := (Client{Key: "routes-key", Endpoint: srv.URL}).Plan(context.Background(), "wholesale", stops)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed || got.Skipped {
		t.Fatalf("plan = %+v", got)
	}
	if got.Polyline != "overall" || got.Attribution != Attribution {
		t.Fatalf("plan = %+v", got)
	}
	want := []string{"a", "c", "b", "d"}
	if strings.Join(got.StopIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got.StopIDs, want)
	}
	if len(got.Legs) != 3 || got.Legs[0].FromID != "a" || got.Legs[0].ToID != "c" {
		t.Fatalf("legs = %+v", got.Legs)
	}
}

func TestPlanIsolatesRoutesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	got, err := (Client{Key: "k", Endpoint: srv.URL}).Plan(context.Background(), "collab", twoStops())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed || got.Error == "" {
		t.Fatalf("plan = %+v, want a failed plan and a nil error", got)
	}
}

func twoStops() []Stop {
	return []Stop{{ID: "a", Lat: 1, Lng: 2}, {ID: "b", Lat: 3, Lng: 4}}
}
