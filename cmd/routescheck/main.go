// Command routescheck proves the field route works end-to-end using the
// gateway's own computeRoutes call: the same endpoint, the same
// X-Goog-Api-Key and X-Goog-FieldMask headers, and the same two-stop request
// shape. It prints one line classifying the result: key missing, the API not
// enabled on the key's project (403), quota (429), a bad request (400), an
// upstream error (5xx), or ok with the stop count and distance. The key is
// never printed; Google's error body never echoes it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const fieldMask = "routes.polyline.encodedPolyline,routes.optimizedIntermediateWaypointIndex,routes.legs,routes.warnings"

type latLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type waypoint struct {
	Location struct {
		LatLng latLng `json:"latLng"`
	} `json:"location"`
}

type computeRequest struct {
	Origin                waypoint   `json:"origin"`
	Destination           waypoint   `json:"destination"`
	Intermediates         []waypoint `json:"intermediates,omitempty"`
	TravelMode            string     `json:"travelMode"`
	OptimizeWaypointOrder bool       `json:"optimizeWaypointOrder"`
}

type leg struct {
	DistanceMeters int `json:"distanceMeters"`
}

type route struct {
	Legs []leg `json:"legs"`
}

type computeResponse struct {
	Routes []route `json:"routes"`
}

func main() {
	key := os.Getenv("GOOGLE_ROUTES_API_KEY")
	if key == "" {
		fmt.Println("key missing")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	reqBody := computeRequest{
		Origin:                point(30.2672, -97.7431),
		Destination:           point(30.271, -97.7501),
		TravelMode:            "DRIVE",
		OptimizeWaypointOrder: true,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Printf("request build failed: %v\n", err)
		os.Exit(1)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://routes.googleapis.com/directions/v2:computeRoutes", bytes.NewReader(raw))
	if err != nil {
		fmt.Printf("request build failed: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", fieldMask)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		fmt.Printf("unreachable: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		var parsed computeResponse
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
			fmt.Printf("ok: unparseable (%v)\n", jsonErr)
			os.Exit(1)
		}
		stops := 0
		meters := 0
		for _, route := range parsed.Routes {
			stops = len(route.Legs) + 1
			for _, leg := range route.Legs {
				meters += leg.DistanceMeters
			}
		}
		fmt.Printf("ok: %d stops, %.1f km\n", stops, float64(meters)/1000)
	case resp.StatusCode == http.StatusForbidden:
		fmt.Printf("api not enabled (403): %s\n", strings.TrimSpace(string(body)))
	case resp.StatusCode == http.StatusTooManyRequests:
		fmt.Printf("quota (429): %s\n", strings.TrimSpace(string(body)))
	case resp.StatusCode == http.StatusBadRequest:
		fmt.Printf("bad request (400): %s\n", strings.TrimSpace(string(body)))
	case resp.StatusCode >= 500:
		fmt.Printf("upstream error (5xx): %s\n", strings.TrimSpace(string(body)))
	default:
		fmt.Printf("upstream error (HTTP %d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func point(lat, lng float64) waypoint {
	var w waypoint
	w.Location.LatLng = latLng{Latitude: lat, Longitude: lng}
	return w
}
