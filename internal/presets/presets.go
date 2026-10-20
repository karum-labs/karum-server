// Package presets is the gateway's copy of the four locked judge campaigns.
//
// The ids, points, radii, category tags, and skip-candidate ids match
// karum-infra/fixtures/presets. Fixture and live modes share these ids. A
// preset click posts the stored point and does not call the geocoder.
package presets

// Preset is one judge button.
type Preset struct {
	ID               string
	Label            string
	Mode             string
	Brand            string
	City             string
	Lat              float64
	Lng              float64
	RadiusMeters     int
	HeatmapRadius    int
	CategoryTags     []string
	Seed             []Entity
	SkipCandidateIDs []string
}

// Entity is one confirmed seed row.
type Entity struct {
	ID   string
	Name string
	Type string
}

// Catalog is the four presets, keyed by id.
type Catalog struct {
	byID map[string]Preset
}

// New returns the locked catalog.
func New() *Catalog {
	items := []Preset{
		{
			ID: "austin-wholesale", Label: "Sola in Austin", Mode: "wholesale",
			Brand: "Sola", City: "Austin, TX, US", Lat: 30.2672, Lng: -97.7431,
			RadiusMeters: 8000, CategoryTags: []string{"urn:tag:genre:place:restaurant:bar"},
			Seed: []Entity{
				{ID: "32F867B9-E6DF-44C3-BCE4-40160C7ED5E4", Name: "Aesop", Type: "urn:entity:brand"},
				{ID: "41C4BB40-E667-416F-9541-63FD7E7FFFF5", Name: "Ilegal Mezcal", Type: "urn:entity:brand"},
				{ID: "BAF6317A-DF94-4C90-92AE-63F86560C21B", Name: "Khruangbin", Type: "urn:entity:artist"},
			},
			// The batch the skip probe scored, not the ones that fell below
			// the floor: Franklin Barbecue was inside the returned range and
			// the taste service drops it on its own. Sending the four the
			// recording skipped would ask a different question than the one
			// that was recorded.
			SkipCandidateIDs: []string{
				"AD3677DA-5D2D-4FBD-8DCA-93D7ED616938",
				"314D0217-CAF0-4758-AB13-6C5CEB355431",
				"572A6EDD-A341-4C1D-8316-6DB97A55BE78",
				"AFB7D16C-D6E0-482D-9A12-41C17D9A9CCF",
				"CE9EE2BD-D7AE-4349-B627-3D051D98AE77",
			},
		},
		{
			ID: "brooklyn-flagship", Label: "Ceramics and matcha in Brooklyn", Mode: "flagship",
			Brand: "Muji", City: "Brooklyn, NY, US", Lat: 40.7172, Lng: -73.9566,
			RadiusMeters: 8000, HeatmapRadius: 10000,
			CategoryTags: []string{"urn:tag:genre:place:restaurant:cafe"},
			Seed: []Entity{
				{ID: "E12201A5-CC50-40AF-97AE-C54A2CA303F7", Name: "Muji", Type: "urn:entity:brand"},
				{ID: "2B5888A0-8A41-4F23-9E1E-0F52DA29244A", Name: "Choplet Ceramic Studio & Gallery", Type: "urn:entity:place"},
				{ID: "6E449EFE-6006-4023-8EEC-0F1F720BDC94", Name: "BKLYN CLAY", Type: "urn:entity:place"},
				{ID: "98F7EBDF-987C-4741-B6F8-5C7511DC0021", Name: "Kettl Tea", Type: "urn:entity:place"},
			},
			SkipCandidateIDs: []string{
				"55E6BA87-F930-4AF0-AD01-D2A3AF0CF782",
				"9B68AA9D-BDA3-479D-95AF-C057CA0F2BEA",
				"1F7B4F4E-2443-4CEE-9B23-E3E54142A8AE",
				"9A47479B-EBCC-4406-9174-220EBC59B691",
				"46B3218C-4B68-4CA4-B871-9C2CB59DBEF6",
				"AF59EF83-C53B-45AB-A980-D5EEECB3A8F0",
				"94ED4726-A194-4DD1-8C8E-04741512A1E4",
				"1EB1DDC1-0907-46FC-8D07-DB1BBCD020CA",
			},
		},
		{
			ID: "brooklyn-collab", Label: "Brooklyn collab", Mode: "collab",
			Brand: "Muji", City: "Brooklyn, NY, US", Lat: 40.7172, Lng: -73.9566,
			RadiusMeters: 8000,
			CategoryTags: []string{"urn:tag:genre:place:art_gallery", "urn:tag:genre:place:ceramics"},
			Seed: []Entity{
				{ID: "E12201A5-CC50-40AF-97AE-C54A2CA303F7", Name: "Muji", Type: "urn:entity:brand"},
				{ID: "2B5888A0-8A41-4F23-9E1E-0F52DA29244A", Name: "Choplet Ceramic Studio & Gallery", Type: "urn:entity:place"},
				{ID: "6E449EFE-6006-4023-8EEC-0F1F720BDC94", Name: "BKLYN CLAY", Type: "urn:entity:place"},
				{ID: "98F7EBDF-987C-4741-B6F8-5C7511DC0021", Name: "Kettl Tea", Type: "urn:entity:place"},
			},
		},
		{
			ID: "mumbai-colaba-wholesale", Label: "Cacao and coffee in Colaba", Mode: "wholesale",
			Brand: "Subko", City: "Mumbai, MH, IN", Lat: 18.93, Lng: 72.83,
			RadiusMeters: 8000, CategoryTags: []string{"urn:tag:genre:place:restaurant:cafe"},
			Seed: []Entity{
				{ID: "0DE2E903-CCB5-4523-BAF0-3EC15514248C", Name: "Mary Lodge by Subko", Type: "urn:entity:place"},
				{ID: "6CEDF620-266F-4B72-B14E-875532C321B1", Name: "Blue Tokai Coffee Roasters | Bandra", Type: "urn:entity:place"},
				{ID: "617683A6-DCF8-4104-8A1A-7484D1F9601C", Name: "Blue Tokai Coffee Roasters | Versova", Type: "urn:entity:place"},
			},
			SkipCandidateIDs: []string{
				"68A744F0-1CE7-4C73-9A60-80C7A5713F72",
				"AE5CB53B-F858-4FFC-8D87-2DDD803A9B2F",
				"41966C27-754D-4135-931F-64CF4F2418CA",
				"75C0A119-48D6-4E7A-B719-47269354E130",
				"F13D82F2-95EF-49CF-8EEF-7B838CA53668",
			},
		},
	}
	byID := make(map[string]Preset, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	return &Catalog{byID: byID}
}

// Get returns a preset by id.
func (c *Catalog) Get(id string) (Preset, bool) {
	if c == nil {
		return Preset{}, false
	}
	item, ok := c.byID[id]
	return item, ok
}

// All returns the four presets in button order.
func (c *Catalog) All() []Preset {
	if c == nil {
		return nil
	}
	order := []string{"austin-wholesale", "brooklyn-flagship", "brooklyn-collab", "mumbai-colaba-wholesale"}
	out := make([]Preset, 0, len(order))
	for _, id := range order {
		if item, ok := c.byID[id]; ok {
			out = append(out, item)
		}
	}
	return out
}

// CampaignID is the stable id shared by fixture and live modes.
func CampaignID(presetID string) string {
	return "cmp-" + presetID
}
