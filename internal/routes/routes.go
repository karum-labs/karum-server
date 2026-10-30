// Package routes orders qualified wholesale and collab doors through the
// Google Routes API. Flagship mode, a missing key, and a set of fewer than
// two geocoded stops skip the call. A Routes error is returned on the plan
// and does not by itself fail the campaign. The response carries the Google
// attribution the map is required to show.
package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Attribution is the credit stored beside a Routes payload.
const Attribution = "Map data © Google"

// NewHTTPClient returns an HTTP client that dials IPv4 only. routes.googleapis.com
// publishes AAAA records, and a host with a DNS answer but no IPv6 route (a
// Docker bridge or a VM without v6 egress) fails every call with "network is
// unreachable" instead of falling back.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

// APIError is a non-2xx response from computeRoutes. The status and body are
// carried so the caller can decide whether to retry (429 and 5xx are
// transient; 4xx is permanent).
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("routes status %d: %s", e.Status, e.Body)
}

// StatusCode reports the HTTP status so the resilience layer can treat 429
// and 5xx as transient and 4xx as permanent.
func (e *APIError) StatusCode() int {
	return e.Status
}

// Stop is one door that can be a waypoint.
type Stop struct {
	ID   string
	Name string
	Lat  float64
	Lng  float64
}

// Leg is one hop the Flutter map can draw.
type Leg struct {
	FromID         string
	ToID           string
	DistanceMeters int
	Duration       string
	Polyline       string
}

// Plan is the field route, or the reason it was not built.
type Plan struct {
	Skipped     bool
	SkipReason  string
	Failed      bool
	Error       string
	Polyline    string
	Legs        []Leg
	StopIDs     []string
	Attribution string
}

// Planner builds a field route.
type Planner interface {
	Plan(ctx context.Context, mode string, stops []Stop) (Plan, error)
}

// Client calls computeRoutes with waypoint optimization.
type Client struct {
	Key      string
	Endpoint string
	HTTP     *http.Client
}

type computeRequest struct {
	Origin                waypoint   `json:"origin"`
	Destination           waypoint   `json:"destination"`
	Intermediates         []waypoint `json:"intermediates,omitempty"`
	TravelMode            string     `json:"travelMode"`
	OptimizeWaypointOrder bool       `json:"optimizeWaypointOrder"`
}

type waypoint struct {
	Location struct {
		LatLng latLng `json:"latLng"`
	} `json:"location"`
}

type latLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type computeResponse struct {
	Routes []struct {
		Polyline struct {
			EncodedPolyline string `json:"encodedPolyline"`
		} `json:"polyline"`
		Optimized []int `json:"optimizedIntermediateWaypointIndex"`
		Legs      []struct {
			DistanceMeters int    `json:"distanceMeters"`
			Duration       string `json:"duration"`
			Polyline       struct {
				EncodedPolyline string `json:"encodedPolyline"`
			} `json:"polyline"`
		} `json:"legs"`
		Warnings []string `json:"warnings"`
	} `json:"routes"`
}

// Plan implements Planner. Skip cases return a plan and a nil error so the
// caller can keep the campaign ready. An HTTP failure returns a failed plan
// and a nil error for the same reason; the error text rides on the plan.
func (c Client) Plan(ctx context.Context, mode string, stops []Stop) (Plan, error) {
	if mode == "flagship" {
		return Plan{Skipped: true, SkipReason: "flagship mode has no field route"}, nil
	}
	if c.Key == "" {
		return Plan{Skipped: true, SkipReason: "routes key is not configured"}, nil
	}
	located := make([]Stop, 0, len(stops))
	for _, stop := range stops {
		if stop.Lat == 0 && stop.Lng == 0 {
			continue
		}
		located = append(located, stop)
	}
	if len(located) < 2 {
		return Plan{Skipped: true, SkipReason: "fewer than two geocoded stops"}, nil
	}
	return c.compute(ctx, located)
}

func (c Client) compute(ctx context.Context, stops []Stop) (Plan, error) {
	origin := stops[0]
	destination := stops[len(stops)-1]
	intermediates := stops[1 : len(stops)-1]
	reqBody := computeRequest{
		Origin:                point(origin),
		Destination:           point(destination),
		TravelMode:            "DRIVE",
		OptimizeWaypointOrder: true,
	}
	for _, stop := range intermediates {
		reqBody.Intermediates = append(reqBody.Intermediates, point(stop))
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return Plan{}, fmt.Errorf("routes request: %w", err)
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://routes.googleapis.com/directions/v2:computeRoutes"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Plan{}, fmt.Errorf("routes request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", c.Key)
	req.Header.Set("X-Goog-FieldMask", "routes.polyline.encodedPolyline,routes.optimizedIntermediateWaypointIndex,routes.legs,routes.warnings")
	client := c.HTTP
	if client == nil {
		client = NewHTTPClient(5 * time.Second)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Plan{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Plan{Failed: true, Error: err.Error(), Attribution: Attribution}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Plan{}, &APIError{Status: resp.StatusCode, Body: string(payload)}
	}
	var parsed computeResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return Plan{Failed: true, Error: err.Error(), Attribution: Attribution}, nil
	}
	if len(parsed.Routes) == 0 {
		return Plan{Failed: true, Error: "routes returned no route", Attribution: Attribution}, nil
	}
	route := parsed.Routes[0]
	ordered := orderStops(stops, route.Optimized)
	legs := make([]Leg, 0, len(route.Legs))
	for i, leg := range route.Legs {
		item := Leg{
			DistanceMeters: leg.DistanceMeters,
			Duration:       leg.Duration,
			Polyline:       leg.Polyline.EncodedPolyline,
		}
		if i < len(ordered)-1 {
			item.FromID = ordered[i].ID
			item.ToID = ordered[i+1].ID
		}
		legs = append(legs, item)
	}
	ids := make([]string, len(ordered))
	for i, stop := range ordered {
		ids[i] = stop.ID
	}
	return Plan{
		Polyline:    route.Polyline.EncodedPolyline,
		Legs:        legs,
		StopIDs:     ids,
		Attribution: Attribution,
	}, nil
}

func orderStops(stops []Stop, optimized []int) []Stop {
	if len(stops) < 2 {
		return stops
	}
	origin := stops[0]
	destination := stops[len(stops)-1]
	middle := stops[1 : len(stops)-1]
	orderedMiddle := make([]Stop, 0, len(middle))
	if len(optimized) == len(middle) {
		for _, idx := range optimized {
			if idx >= 0 && idx < len(middle) {
				orderedMiddle = append(orderedMiddle, middle[idx])
			}
		}
	}
	if len(orderedMiddle) != len(middle) {
		orderedMiddle = append([]Stop(nil), middle...)
	}
	out := make([]Stop, 0, len(stops))
	out = append(out, origin)
	out = append(out, orderedMiddle...)
	out = append(out, destination)
	return out
}

func point(stop Stop) waypoint {
	var wp waypoint
	wp.Location.LatLng = latLng{Latitude: stop.Lat, Longitude: stop.Lng}
	return wp
}
