package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GoogleGeocoder resolves a city through the Google Geocoding API.
//
// One result is stored. More than one result is ambiguous: the candidates are
// returned and Resolved stays unset. Lookups are cached. The attribution
// string is kept on the result so the client can show it.
type GoogleGeocoder struct {
	// Key is the Geocoding API key. An empty key fails closed with a
	// validation-shaped upstream message rather than inventing a point.
	Key string
	// Endpoint overrides the Geocoding URL in tests.
	Endpoint string
	// HTTP is the client. Nil uses a short default timeout.
	HTTP *http.Client
	// Cache stores successes and ambiguous results. Nil skips the cache.
	Cache Cache
}

type googleResponse struct {
	Status  string         `json:"status"`
	Results []googleResult `json:"results"`
	Error   string         `json:"error_message"`
}

type googleResult struct {
	Formatted string `json:"formatted_address"`
	Geometry  struct {
		Location struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
	} `json:"geometry"`
}

// Geocode implements Geocoder.
func (g GoogleGeocoder) Geocode(ctx context.Context, query string) (*Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, &ValidationError{Message: "city query is empty"}
	}
	if g.Cache != nil {
		if hit, ok := g.Cache.Get(ctx, q); ok {
			return &hit, nil
		}
	}
	if g.Key == "" {
		return nil, fmt.Errorf("geocoder has no API key")
	}

	endpoint := g.Endpoint
	if endpoint == "" {
		endpoint = "https://maps.googleapis.com/maps/api/geocode/json"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("geocoder endpoint: %w", err)
	}
	params := u.Query()
	params.Set("address", q)
	params.Set("key", g.Key)
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoder: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geocoder body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocoder status %d", resp.StatusCode)
	}
	var payload googleResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("geocoder json: %w", err)
	}
	result, err := interpretGoogle(payload)
	if err != nil {
		return nil, err
	}
	if g.Cache != nil {
		g.Cache.Set(ctx, q, *result)
	}
	return result, nil
}

func interpretGoogle(payload googleResponse) (*Result, error) {
	switch payload.Status {
	case "OK":
	case "ZERO_RESULTS":
		return nil, &ValidationError{Message: "city query did not resolve"}
	case "INVALID_REQUEST":
		return nil, &ValidationError{Message: "city query was rejected by the geocoder"}
	default:
		msg := payload.Error
		if msg == "" {
			msg = payload.Status
		}
		return nil, fmt.Errorf("geocoder: %s", msg)
	}
	if len(payload.Results) == 0 {
		return nil, &ValidationError{Message: "city query did not resolve"}
	}
	if len(payload.Results) > 1 {
		cands := make([]Candidate, 0, len(payload.Results))
		for _, row := range payload.Results {
			if err := ValidatePoint(row.Geometry.Location.Lat, row.Geometry.Location.Lng); err != nil {
				continue
			}
			cands = append(cands, Candidate{
				FormattedAddress: row.Formatted,
				Point: Point{
					Lat: row.Geometry.Location.Lat,
					Lng: row.Geometry.Location.Lng,
				},
			})
		}
		if len(cands) == 0 {
			return nil, &ValidationError{Message: "geocoder returned unusable coordinates"}
		}
		if len(cands) == 1 {
			return &Result{
				Resolved:         cands[0].Point,
				FormattedAddress: cands[0].FormattedAddress,
				Attribution:      AttributionGoogle,
			}, nil
		}
		return &Result{Candidates: cands, Attribution: AttributionGoogle}, nil
	}
	row := payload.Results[0]
	if err := ValidatePoint(row.Geometry.Location.Lat, row.Geometry.Location.Lng); err != nil {
		return nil, &ValidationError{Message: "geocoder returned unusable coordinates"}
	}
	return &Result{
		Resolved:         Point{Lat: row.Geometry.Location.Lat, Lng: row.Geometry.Location.Lng},
		FormattedAddress: row.Formatted,
		Attribution:      AttributionGoogle,
	}, nil
}
