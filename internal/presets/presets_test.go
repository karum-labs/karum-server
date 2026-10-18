package presets

import "testing"

func TestCatalogHasTheFourLockedPresets(t *testing.T) {
	cat := New()
	all := cat.All()
	if len(all) != 4 {
		t.Fatalf("presets = %d, want 4", len(all))
	}
	austin, ok := cat.Get("austin-wholesale")
	if !ok {
		t.Fatal("missing austin")
	}
	if austin.Lat != 30.2672 || austin.RadiusMeters != 8000 {
		t.Fatalf("austin point/radius = %v %d", austin, austin.RadiusMeters)
	}
	if len(austin.Seed) != 3 || austin.Seed[1].Name != "Ilegal Mezcal" {
		t.Fatalf("austin seed = %+v", austin.Seed)
	}
	flagship, _ := cat.Get("brooklyn-flagship")
	if flagship.HeatmapRadius != 10000 || flagship.Mode != "flagship" {
		t.Fatalf("flagship = %+v", flagship)
	}
	collab, _ := cat.Get("brooklyn-collab")
	if len(collab.CategoryTags) != 2 || collab.Mode != "collab" {
		t.Fatalf("collab tags = %v", collab.CategoryTags)
	}
	if _, ok := cat.Get("nope"); ok {
		t.Fatal("unknown preset resolved")
	}
	if CampaignID("austin-wholesale") != "cmp-austin-wholesale" {
		t.Fatal(CampaignID("austin-wholesale"))
	}
}
