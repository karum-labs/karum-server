package places

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMissingKeyMarksEnrichmentUnknown(t *testing.T) {
	got, err := (Client{}).Enrich(context.Background(), []Target{{ID: "d-1", Name: "Whisler's"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Unknown {
		t.Fatalf("enrichment = %+v, want unknown", got)
	}
}

func TestEnrichmentCarriesWebsiteAndClosed(t *testing.T) {
	var sawKey, sawMask bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("X-Goog-Api-Key") == "places-key"
		mask := r.Header.Get("X-Goog-FieldMask")
		sawMask = strings.Contains(mask, "websiteUri") &&
			strings.Contains(mask, "nationalPhoneNumber") &&
			strings.Contains(mask, "internationalPhoneNumber")
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		if !strings.Contains(text, "Whisler") && !strings.Contains(text, "Closed Bar") {
			t.Errorf("body = %s", text)
		}
		if strings.Contains(string(body), "Closed Bar") {
			_, _ = w.Write([]byte(`{"places":[{"formattedAddress":"1 Sixth St","businessStatus":"CLOSED_PERMANENTLY","websiteUri":"https://example.test","internationalPhoneNumber":"+1 555-0100"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"places":[{"formattedAddress":"1314 E 6th St","businessStatus":"OPERATIONAL","websiteUri":"https://whislers.example","location":{"latitude":30.26,"longitude":-97.73},"regularOpeningHours":{"weekdayDescriptions":["Monday: 4:00 PM – 2:00 AM"]},"nationalPhoneNumber":"(512) 555-0100","internationalPhoneNumber":"+1 512-555-0100"}]}`))
	}))
	defer srv.Close()

	client := Client{Key: "places-key", Endpoint: srv.URL}
	got, err := client.Enrich(context.Background(), []Target{
		{ID: "d-1", Name: "Whisler's", City: "Austin", Lat: 30.2, Lng: -97.7},
		{ID: "d-2", Name: "Closed Bar", City: "Austin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawKey || !sawMask {
		t.Fatalf("key=%v mask=%v", sawKey, sawMask)
	}
	if got[0].Unknown || got[0].Website != "https://whislers.example" || got[0].Phone != "+1 512-555-0100" || got[0].Closed {
		t.Fatalf("open door = %+v", got[0])
	}
	if !strings.Contains(got[0].Hours, "Monday") {
		t.Fatalf("hours = %q", got[0].Hours)
	}
	if !got[1].Closed || got[1].Unknown {
		t.Fatalf("closed door = %+v", got[1])
	}
	if got[0].Attribution != Attribution {
		t.Fatalf("attribution = %q", got[0].Attribution)
	}
}

func TestPlacesErrorLeavesUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	got, err := (Client{Key: "k", Endpoint: srv.URL}).Enrich(context.Background(), []Target{{ID: "d-1", Name: "Whisler's"}})
	if err == nil {
		t.Fatal("expected an error when every lookup failed")
	}
	if len(got) != 1 || !got[0].Unknown {
		t.Fatalf("enrichment = %+v", got)
	}
}
