// Package places enriches the working door set through the Places API (New).
//
// A missing key or a Places error leaves every door in place with enrichment
// marked unknown. A venue Places reports as permanently closed is flagged so
// the runner can skip it. The website is the hint the menu audit searches
// from. Grounding URLs are not fetched here.
package places

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Attribution is the credit the campaign payload carries for Places data.
const Attribution = "Powered by Google"

// Target is one door the enricher looks up.
type Target struct {
	ID   string
	Name string
	City string
	Lat  float64
	Lng  float64
}

// Enrichment is the Places read for one door.
type Enrichment struct {
	ID         string
	Address    string
	Hours      string
	Website    string
	OpenStatus string
	Lat        float64
	Lng        float64
	// Closed is true when Places reports the venue permanently closed.
	Closed bool
	// Unknown is true when the key is missing, Places errored, or the venue
	// was not found. The door stays on the board.
	Unknown     bool
	Attribution string
}

// Enricher looks up the working set.
type Enricher interface {
	Enrich(ctx context.Context, targets []Target) ([]Enrichment, error)
}

// Client calls Places API (New) text search.
type Client struct {
	Key      string
	Endpoint string
	HTTP     *http.Client
}

type searchRequest struct {
	TextQuery    string        `json:"textQuery"`
	PageSize     int           `json:"pageSize"`
	LocationBias *locationBias `json:"locationBias,omitempty"`
}

type locationBias struct {
	Circle circle `json:"circle"`
}

type circle struct {
	Center latLng  `json:"center"`
	Radius float64 `json:"radius"`
}

type latLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type searchResponse struct {
	Places []placeResource `json:"places"`
}

type placeResource struct {
	FormattedAddress string `json:"formattedAddress"`
	WebsiteURI       string `json:"websiteUri"`
	BusinessStatus   string `json:"businessStatus"`
	Location         latLng `json:"location"`
	Hours            struct {
		WeekdayDescriptions []string `json:"weekdayDescriptions"`
	} `json:"regularOpeningHours"`
}

// Enrich implements Enricher. A missing key returns an unknown enrichment per
// target and no error, so the job can still reach ready.
func (c Client) Enrich(ctx context.Context, targets []Target) ([]Enrichment, error) {
	out := make([]Enrichment, len(targets))
	if c.Key == "" {
		for i, target := range targets {
			out[i] = unknown(target.ID)
		}
		return out, nil
	}
	var firstErr error
	for i, target := range targets {
		item, err := c.one(ctx, target)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			out[i] = unknown(target.ID)
			continue
		}
		out[i] = item
	}
	if firstErr != nil && allUnknown(out) {
		return out, firstErr
	}
	return out, nil
}

func (c Client) one(ctx context.Context, target Target) (Enrichment, error) {
	query := strings.TrimSpace(target.Name)
	if target.City != "" {
		query = strings.TrimSpace(query + ", " + target.City)
	}
	if query == "" {
		return unknown(target.ID), nil
	}
	body := searchRequest{TextQuery: query, PageSize: 1}
	if target.Lat != 0 || target.Lng != 0 {
		body.LocationBias = &locationBias{Circle: circle{
			Center: latLng{Latitude: target.Lat, Longitude: target.Lng},
			Radius: 5000,
		}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Enrichment{}, err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://places.googleapis.com/v1/places:searchText"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Enrichment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", c.Key)
	req.Header.Set("X-Goog-FieldMask", "places.formattedAddress,places.websiteUri,places.businessStatus,places.location,places.regularOpeningHours")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Enrichment{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Enrichment{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Enrichment{}, fmt.Errorf("places status %d", resp.StatusCode)
	}
	var parsed searchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return Enrichment{}, err
	}
	if len(parsed.Places) == 0 {
		return unknown(target.ID), nil
	}
	row := parsed.Places[0]
	status := strings.ToLower(row.BusinessStatus)
	if status == "" {
		status = "unknown"
	}
	return Enrichment{
		ID:          target.ID,
		Address:     row.FormattedAddress,
		Hours:       strings.Join(row.Hours.WeekdayDescriptions, "; "),
		Website:     row.WebsiteURI,
		OpenStatus:  status,
		Lat:         row.Location.Latitude,
		Lng:         row.Location.Longitude,
		Closed:      row.BusinessStatus == "CLOSED_PERMANENTLY",
		Attribution: Attribution,
	}, nil
}

func unknown(id string) Enrichment {
	return Enrichment{ID: id, OpenStatus: "unknown", Unknown: true, Attribution: Attribution}
}

func allUnknown(items []Enrichment) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !item.Unknown {
			return false
		}
	}
	return true
}
