package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/resilience"
)

// upstream stands in for places.googleapis.com and records what it was asked.
type upstream struct {
	mu      sync.Mutex
	calls   int
	path    string
	query   string
	mask    string
	apiKey  string
	body    map[string]any
	status  int
	answer  string
	mime    string
	rawBody []byte
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.path = r.URL.Path
	u.query = r.URL.RawQuery
	u.mask = r.Header.Get("X-Goog-FieldMask")
	u.apiKey = r.Header.Get("X-Goog-Api-Key")
	u.rawBody, _ = io.ReadAll(r.Body)
	u.body = nil
	_ = json.Unmarshal(u.rawBody, &u.body)
	if u.mime != "" {
		w.Header().Set("Content-Type", u.mime)
	}
	if u.status != 0 {
		w.WriteHeader(u.status)
	}
	_, _ = w.Write([]byte(u.answer))
}

func placesServer(t *testing.T, up *upstream, limiter *resilience.Limiter) *httptest.Server {
	t.Helper()
	fake := httptest.NewServer(up)
	t.Cleanup(fake.Close)
	router := NewRouterWithDeps(
		&config.Config{Mode: config.ModeFixture, Version: "test", PlacesAPIKey: "SECRET-KEY"},
		Dependencies{PlacesBaseURL: fake.URL, PlacesLimiter: limiter},
	)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

const fullPlace = `{"places":[{
  "id":"abc",
  "displayName":{"text":"Whisler's"},
  "formattedAddress":"1816 E 6th St, Austin, TX",
  "internationalPhoneNumber":"+1 512-480-0781",
  "websiteUri":"https://whislers.com",
  "googleMapsUri":"https://maps.google.com/?cid=1",
  "rating":4.5,"userRatingCount":1234,
  "priceLevel":"PRICE_LEVEL_MODERATE",
  "primaryTypeDisplayName":{"text":"Cocktail bar"},
  "editorialSummary":{"text":"Mezcal-forward bar with a garden."},
  "utcOffsetMinutes":-300,
  "regularOpeningHours":{"openNow":true,"weekdayDescriptions":["Monday: 4 PM - 2 AM","Tuesday: 4 PM - 2 AM"]},
  "reviews":[
    {"rating":5,"relativePublishTimeDescription":"3 weeks ago","text":{"text":"Great mezcal list."},"authorAttribution":{"displayName":"Ana"}},
    {"rating":4,"relativePublishTimeDescription":"a month ago","text":{"text":""},"authorAttribution":{"displayName":"Blank"}}
  ],
  "photos":[{"name":"places/abc/photos/p1","authorAttributions":[{"displayName":"Sam"}]}]
}]}`

func TestPlaceDetailsShapesTheAnswerAndOwnsTheFieldMask(t *testing.T) {
	up := &upstream{answer: fullPlace, mime: "application/json"}
	srv := placesServer(t, up, nil)

	// A caller-supplied `fields` list must not reach Google.
	resp, err := http.Get(srv.URL + "/api/v1/places/details?place_name=Whisler%27s&lat=30.26&lng=-97.72&fields=reviews,photos,atmosphere")
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, resp)
	if resp.StatusCode != http.StatusOK || got["status"] != "OK" {
		t.Fatalf("status=%d body=%+v", resp.StatusCode, got)
	}
	if up.mask != placesFieldMask {
		t.Fatalf("field mask = %q, want the gateway's own", up.mask)
	}
	if strings.Contains(up.mask, "atmosphere") {
		t.Fatal("the caller's fields reached the upstream mask")
	}
	if up.apiKey != "SECRET-KEY" {
		t.Fatalf("api key header = %q", up.apiKey)
	}

	result, _ := got["result"].(map[string]any)
	for key, want := range map[string]any{
		"name":                   "Whisler's",
		"formatted_phone_number": "+1 512-480-0781",
		"website":                "https://whislers.com",
		"rating":                 4.5,
		"rating_count":           float64(1234),
		"price_level":            float64(2),
		"category":               "Cocktail bar",
		"summary":                "Mezcal-forward bar with a garden.",
		"open_now":               true,
		"utc_offset_minutes":     float64(-300),
	} {
		if result[key] != want {
			t.Errorf("result[%q] = %v, want %v", key, result[key], want)
		}
	}
	reviews, _ := result["reviews"].([]any)
	if len(reviews) != 1 {
		t.Fatalf("reviews = %+v, want the blank one dropped", result["reviews"])
	}
	first, _ := reviews[0].(map[string]any)
	if first["author"] != "Ana" || first["relative_time"] != "3 weeks ago" || first["text"] != "Great mezcal list." {
		t.Fatalf("review = %+v", first)
	}
	photos, _ := result["photos"].([]any)
	photo, _ := photos[0].(map[string]any)
	if photo["name"] != "places/abc/photos/p1" || photo["author"] != "Sam" {
		t.Fatalf("photo = %+v", photo)
	}
}

func TestPlaceDetailsRestrictsTheSearchToTheTappedPin(t *testing.T) {
	up := &upstream{answer: fullPlace, mime: "application/json"}
	srv := placesServer(t, up, nil)

	resp, err := http.Get(srv.URL + "/api/v1/places/details?place_name=Whisler%27s&lat=30.26&lng=-97.72")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	restriction, ok := up.body["locationRestriction"].(map[string]any)
	if !ok {
		t.Fatalf("body = %+v, want a locationRestriction", up.body)
	}
	rect, _ := restriction["rectangle"].(map[string]any)
	low, _ := rect["low"].(map[string]any)
	high, _ := rect["high"].(map[string]any)
	if low["latitude"].(float64) >= 30.26 || high["latitude"].(float64) <= 30.26 ||
		low["longitude"].(float64) >= -97.72 || high["longitude"].(float64) <= -97.72 {
		t.Fatalf("the pin is not inside the search box: low=%v high=%v", low, high)
	}

	// A zero pair is the client's "no point" and must not restrict anything.
	up.body = nil
	resp, err = http.Get(srv.URL + "/api/v1/places/details?place_name=Whisler%27s&lat=0&lng=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, has := up.body["locationRestriction"]; has {
		t.Fatalf("a 0,0 pin restricted the search: %+v", up.body)
	}
}

func TestPlaceDetailsNotFoundAndUpstreamFailureAreDistinct(t *testing.T) {
	up := &upstream{answer: `{}`, mime: "application/json"}
	srv := placesServer(t, up, nil)

	resp, err := http.Get(srv.URL + "/api/v1/places/details?place_name=Nowhere")
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, resp)
	if resp.StatusCode != http.StatusOK || got["status"] != "NOT_FOUND" {
		t.Fatalf("not found: status=%d body=%+v", resp.StatusCode, got)
	}

	up.status, up.answer = http.StatusForbidden, `{"error":"key revoked SECRET-KEY"}`
	resp, err = http.Get(srv.URL + "/api/v1/places/details?place_name=Nowhere")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("refused upstream = %d", resp.StatusCode)
	}
	if strings.Contains(string(raw), "SECRET-KEY") {
		t.Fatalf("the upstream's answer leaked to the visitor: %s", raw)
	}
}

func TestPlaceDetailsRejectsAMissingName(t *testing.T) {
	up := &upstream{answer: fullPlace}
	srv := placesServer(t, up, nil)
	resp, err := http.Get(srv.URL + "/api/v1/places/details")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || up.calls != 0 {
		t.Fatalf("status=%d upstream calls=%d", resp.StatusCode, up.calls)
	}
}

func TestPlacePhotoRefusesAnythingButAPlacesPhotoReference(t *testing.T) {
	up := &upstream{answer: "IMG", mime: "image/jpeg"}
	srv := placesServer(t, up, nil)

	for _, bad := range []string{
		"places/abc/photos/../../other",
		"v1/places/abc/photos/p1",
		"places/abc",
		"",
		"places/abc/photos/p1?x=1",
	} {
		resp, err := http.Get(srv.URL + "/api/v1/places/photo?photo_name=" + strings.ReplaceAll(bad, "?", "%3F"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("photo_name %q = %d, want 400", bad, resp.StatusCode)
		}
	}
	if up.calls != 0 {
		t.Fatalf("a refused name still reached the upstream %d time(s)", up.calls)
	}

	resp, err := http.Get(srv.URL + "/api/v1/places/photo?photo_name=places/abc/photos/p1&maxwidth=900")
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, resp)
	data, _ := got["photo_data"].(string)
	if !strings.HasPrefix(data, "data:image/jpeg;base64,") {
		t.Fatalf("photo_data = %q", data)
	}
	if up.path != "/v1/places/abc/photos/p1/media" || up.query != "maxWidthPx=900" {
		t.Fatalf("upstream got %s?%s", up.path, up.query)
	}
	if strings.Contains(up.query, "key=") || up.apiKey != "SECRET-KEY" {
		t.Fatalf("the key must travel in a header, not the URL: query=%q header=%q", up.query, up.apiKey)
	}
}

func TestPlacePhotoClampsTheWidth(t *testing.T) {
	up := &upstream{answer: "IMG", mime: "image/jpeg"}
	srv := placesServer(t, up, nil)
	for _, width := range []string{"50", "5000", "abc"} {
		resp, err := http.Get(srv.URL + "/api/v1/places/photo?photo_name=places/abc/photos/p1&maxwidth=" + width)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("maxwidth=%s = %d, want 400", width, resp.StatusCode)
		}
	}
}

func TestPlacesProxiesAreRateLimited(t *testing.T) {
	up := &upstream{answer: fullPlace, mime: "application/json"}
	// Two lookups of burst, then refusal.
	srv := placesServer(t, up, resilience.NewLimiter(0.001, 2, 100, 100))

	statuses := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		resp, err := http.Get(srv.URL + "/api/v1/places/details?place_name=Whisler%27s")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK || statuses[2] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v, want 200, 200, then 429", statuses)
	}
	if up.calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 (a limited call must not reach Google)", up.calls)
	}

	// The photo route shares the budget, so it is limited too.
	resp, err := http.Get(srv.URL + "/api/v1/places/photo?photo_name=places/abc/photos/p1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("photo while limited = %d", resp.StatusCode)
	}
}
