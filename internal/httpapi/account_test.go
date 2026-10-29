package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/orchestrator"
)

func TestAccountCampaignsListsOwnedCampaigns(t *testing.T) {
	authStore := newAuthTestStore()
	service := auth.NewService(authStore, time.Hour)

	session, err := service.Register(context.Background(), "owner@example.com", "password12345")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if session.Token == "" {
		t.Fatal("register returned no token")
	}

	orchStore := orchestrator.NewStore()
	now := time.Now()
	orchStore.UpsertCampaign(orchestrator.CampaignRecord{
		ID:           "cmp-owned-1",
		PresetID:     "austin-wholesale",
		Mode:         orchestrator.ModeWholesale,
		City:         "Austin, TX, US",
		BrandName:    "Sola",
		RadiusMeters: 8000,
		AccountID:    session.Account.ID,
		CreatedAt:    now,
	})
	orchStore.UpsertCampaign(orchestrator.CampaignRecord{
		ID:           "cmp-owned-2",
		PresetID:     "mumbai-colaba-wholesale",
		Mode:         orchestrator.ModeWholesale,
		City:         "Mumbai, MH, IN",
		BrandName:    "Subko",
		RadiusMeters: 8000,
		AccountID:    session.Account.ID,
		CreatedAt:    now.Add(time.Minute),
	})
	// A campaign owned by nobody else must not appear.
	orchStore.UpsertCampaign(orchestrator.CampaignRecord{
		ID:           "cmp-other",
		PresetID:     "brooklyn-flagship",
		Mode:         orchestrator.ModeFlagship,
		City:         "Brooklyn, NY, US",
		BrandName:    "Muji",
		RadiusMeters: 8000,
		AccountID:    "acct-other",
		CreatedAt:    now,
	})

	router := NewRouterWithDeps(&config.Config{Mode: config.ModeFixture}, Dependencies{
		Auth:  service,
		Store: orchStore,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/campaigns", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Campaigns []accountCampaignBody `json:"campaigns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Campaigns) != 2 {
		t.Fatalf("got %d campaigns, want 2: %+v", len(body.Campaigns), body.Campaigns)
	}
	// Newest first: Mumbai (now+1m) before Austin (now).
	if body.Campaigns[0].ID != "cmp-owned-2" || body.Campaigns[1].ID != "cmp-owned-1" {
		t.Errorf("order = %+v, want newest first", body.Campaigns)
	}
	for _, c := range body.Campaigns {
		if c.ID == "cmp-other" {
			t.Errorf("campaign %s leaked from another account", c.ID)
		}
	}
}

func TestAccountCampaignsRejectsAnonymous(t *testing.T) {
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeFixture}, Dependencies{
		Auth: auth.NewService(newAuthTestStore(), time.Hour),
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/account/campaigns", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", w.Code)
	}
}

func TestAccountCampaignsEmptyWhenNone(t *testing.T) {
	authStore := newAuthTestStore()
	service := auth.NewService(authStore, time.Hour)
	session, err := service.Register(context.Background(), "empty@example.com", "password12345")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	router := NewRouterWithDeps(&config.Config{Mode: config.ModeFixture}, Dependencies{
		Auth:  service,
		Store: orchestrator.NewStore(),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/campaigns", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Campaigns []accountCampaignBody `json:"campaigns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Campaigns) != 0 {
		t.Fatalf("got %d campaigns, want 0", len(body.Campaigns))
	}
}
