package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/presets"
)

// presetPointBody is a latitude/longitude pair, the shape the client's
// `GeoPoint.fromJson` already expects.
type presetPointBody struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// presetSeedBody mirrors the client's `SeedEntity`.
type presetSeedBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// presetSKUBody mirrors the client's `Sku`.
type presetSKUBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// presetBody is one entry in `GET /api/v1/presets`. It is the Preset struct
// as JSON, plus the derived CampaignID a preset click needs to restore a run.
type presetBody struct {
	ID               string           `json:"id"`
	GroupID          string           `json:"group_id"`
	GroupLabel       string           `json:"group_label"`
	GroupDescription string           `json:"group_description"`
	Label            string           `json:"label"`
	ButtonLabel      string           `json:"button_label"`
	Description      string           `json:"description"`
	Brand            string           `json:"brand"`
	City             string           `json:"city"`
	Mode             string           `json:"mode"`
	Point            presetPointBody  `json:"point"`
	RadiusMeters     int              `json:"radius_meters"`
	HeatmapRadius    int              `json:"heatmap_radius,omitempty"`
	CategoryTags     []string         `json:"category_tags"`
	Seeds            []presetSeedBody `json:"seeds"`
	SKUs             []presetSKUBody  `json:"skus"`
	CampaignID       string           `json:"campaign_id"`
}

// getPresets serves the locked judge catalogue as data, so the client reads
// the four previous-run cards from the gateway instead of hand-maintaining a
// second, driftable copy in `presets.dart` (PLAN.md §52). It is public and
// unauthenticated: the catalogue is the same for every visitor and carries
// no account-specific state — unlike `GET /campaigns/:id` (PLAN.md §49),
// nothing here needs scoping.
func (h *Handler) getPresets(c *gin.Context) {
	items := h.catalog.All()
	out := make([]presetBody, 0, len(items))
	for _, item := range items {
		seeds := make([]presetSeedBody, 0, len(item.Seed))
		for _, entity := range item.Seed {
			seeds = append(seeds, presetSeedBody{ID: entity.ID, Name: entity.Name, Type: entity.Type})
		}
		skus := make([]presetSKUBody, 0, len(item.SKUs))
		for _, sku := range item.SKUs {
			skus = append(skus, presetSKUBody{ID: sku.ID, Name: sku.Name})
		}
		out = append(out, presetBody{
			ID: item.ID, GroupID: item.GroupID, GroupLabel: item.GroupLabel,
			GroupDescription: item.GroupDescription, Label: item.Label,
			ButtonLabel: item.ButtonLabel, Description: item.Description,
			Brand: item.Brand, City: item.City, Mode: item.Mode,
			Point:         presetPointBody{Lat: item.Lat, Lng: item.Lng},
			RadiusMeters:  item.RadiusMeters,
			HeatmapRadius: item.HeatmapRadius,
			CategoryTags:  item.CategoryTags,
			Seeds:         seeds,
			SKUs:          skus,
			CampaignID:    presets.CampaignID(item.ID),
		})
	}
	c.JSON(http.StatusOK, gin.H{"presets": out})
}
