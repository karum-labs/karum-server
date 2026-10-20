package httpapi

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/orchestrator"
)

const (
	maxText   = 200
	maxSKUs   = 24
	maxSeeds  = 4
	minRadius = 500
	maxRadius = 50000
)

// scoutBody is the POST /api/v1/scout payload.
type scoutBody struct {
	PresetID         string       `json:"preset_id"`
	FreeEntry        bool         `json:"free_entry"`
	Rerun            bool         `json:"rerun"`
	City             string       `json:"city"`
	Mode             string       `json:"mode"`
	Point            *pointBody   `json:"point"`
	RadiusMeters     int          `json:"radius_meters"`
	BrandName        string       `json:"brand_name"`
	SKUs             []skuBody    `json:"skus"`
	SeedEntities     []entityBody `json:"seed_entities"`
	BrandQuery       string       `json:"brand_query"`
	FallbackQueries  []string     `json:"fallback_queries"`
	CategoryTags     []string     `json:"category_tags"`
	SkipCandidateIDs []string     `json:"skip_candidate_ids"`
}

type pointBody struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type skuBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type entityBody struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

var errLiveRequired = errors.New("free entry requires the live pipeline")

func validateScout(body scoutBody, hasPreset bool) error {
	if !hasPreset && strings.TrimSpace(body.City) == "" && body.Point == nil && strings.TrimSpace(body.BrandQuery) == "" && len(body.SeedEntities) == 0 {
		return errors.New("city is required")
	}
	if err := checkText("city", body.City); err != nil {
		return err
	}
	if err := checkText("brand_name", body.BrandName); err != nil {
		return err
	}
	if err := checkText("brand_query", body.BrandQuery); err != nil {
		return err
	}
	if body.Mode != "" && body.Mode != string(orchestrator.ModeWholesale) && body.Mode != string(orchestrator.ModeFlagship) && body.Mode != string(orchestrator.ModeCollab) {
		return errors.New("mode must be wholesale, flagship, or collab")
	}
	if body.RadiusMeters != 0 && (body.RadiusMeters < minRadius || body.RadiusMeters > maxRadius) {
		return errors.New("radius_meters must be between 500 and 50000")
	}
	if len(body.SKUs) > maxSKUs {
		return errors.New("sku list exceeds 24")
	}
	for _, sku := range body.SKUs {
		if err := checkText("sku", sku.Name); err != nil {
			return err
		}
	}
	queries := 0
	if strings.TrimSpace(body.BrandQuery) != "" {
		queries++
	}
	for _, query := range body.FallbackQueries {
		if err := checkText("fallback_query", query); err != nil {
			return err
		}
		if strings.TrimSpace(query) != "" {
			queries++
		}
	}
	seeds := len(body.SeedEntities)
	if seeds > maxSeeds || queries > maxSeeds {
		return errors.New("a scout accepts one to four seed entities")
	}
	if !hasPreset && !body.FreeEntry && seeds == 0 && queries == 0 {
		return errors.New("a confirmed seed or a brand query is required")
	}
	if body.FreeEntry && seeds == 0 && queries == 0 && !hasPreset {
		return errors.New("free entry needs a seed or a brand query")
	}
	if body.Point != nil {
		if err := geocode.ValidatePoint(body.Point.Lat, body.Point.Lng); err != nil {
			return err
		}
	}
	return nil
}

func checkText(field, value string) error {
	if utf8.RuneCountInString(value) > maxText {
		return errors.New(field + " exceeds 200 characters")
	}
	return nil
}
