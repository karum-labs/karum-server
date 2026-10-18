package geocode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGeocodeRejectsEmptyQuery(t *testing.T) {
	g := GoogleGeocoder{Key: "test"}
	_, err := g.Geocode(context.Background(), "  ")
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want validation", err)
	}
}

func TestValidatePoint(t *testing.T) {
	if err := ValidatePoint(91, 0); err == nil {
		t.Fatal("latitude 91 must be rejected")
	}
	if err := ValidatePoint(0, 181); err == nil {
		t.Fatal("longitude 181 must be rejected")
	}
	if err := ValidatePoint(30.27, -97.74); err != nil {
		t.Fatal(err)
	}
}

func TestGeocodeSingleResultIsCachedWithAttribution(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("key") == "" || r.URL.Query().Get("address") == "" {
			t.Errorf("missing query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"OK","results":[{"formatted_address":"Austin, TX, USA","geometry":{"location":{"lat":30.2672,"lng":-97.7431}}}]}`))
	}))
	defer srv.Close()

	cache := NewMemoryCache()
	g := GoogleGeocoder{Key: "secret", Endpoint: srv.URL, Cache: cache}
	first, err := g.Geocode(context.Background(), "Austin")
	if err != nil {
		t.Fatal(err)
	}
	if first.Resolved.Lat != 30.2672 || first.Attribution != AttributionGoogle {
		t.Fatalf("result = %+v", first)
	}
	if first.CacheHit {
		t.Fatal("first lookup must not be a cache hit")
	}
	second, err := g.Geocode(context.Background(), " austin ")
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit {
		t.Fatal("second lookup must come from the cache")
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits = %d, want 1", hits.Load())
	}
}

func TestGeocodeAmbiguousDoesNotPick(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK","results":[
			{"formatted_address":"Springfield, IL, USA","geometry":{"location":{"lat":39.78,"lng":-89.65}}},
			{"formatted_address":"Springfield, MO, USA","geometry":{"location":{"lat":37.21,"lng":-93.29}}}
		]}`))
	}))
	defer srv.Close()

	g := GoogleGeocoder{Key: "secret", Endpoint: srv.URL, Cache: NewMemoryCache()}
	got, err := g.Geocode(context.Background(), "Springfield")
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolved != (Point{}) {
		t.Fatalf("ambiguous query picked %+v", got.Resolved)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(got.Candidates))
	}
	if got.Attribution != AttributionGoogle {
		t.Fatalf("attribution = %q", got.Attribution)
	}
}

func TestGeocodeZeroResultsIsValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ZERO_RESULTS","results":[]}`))
	}))
	defer srv.Close()
	g := GoogleGeocoder{Key: "secret", Endpoint: srv.URL}
	_, err := g.Geocode(context.Background(), "nowhere-xyz")
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want validation", err)
	}
}

func TestNoopGeocoderStillFailsClosed(t *testing.T) {
	_, err := NoopGeocoder{}.Geocode(context.Background(), "Austin")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("error = %v", err)
	}
}
