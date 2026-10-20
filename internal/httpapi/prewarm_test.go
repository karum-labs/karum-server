package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/config"
	"github.com/karum-labs/karum-server/internal/orchestrator"
)

func prewarmRequest(t *testing.T, srv *httptest.Server, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/prewarm", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPrewarmStoresEveryPresetAndReusesTheStoredRun(t *testing.T) {
	store := orchestrator.NewStore()
	service := auth.NewService(newAuthTestStore(), time.Hour)
	session, err := service.JudgeLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := orchestrator.NewRunner(&httpTaste{name: "Prewarmed Door"}, httpAgent{}, store)
	srv := httptest.NewServer(NewRouterWithDeps(&config.Config{
		Mode: config.ModeFixture, Version: "test",
	}, Dependencies{
		Live: runner, Fixture: runner, Store: store, Auth: service,
	}))
	defer srv.Close()

	resp := prewarmRequest(t, srv, session.Token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, decode(t, resp))
	}
	first := decode(t, resp)
	presets, _ := first["presets"].([]any)
	if len(presets) != 4 {
		t.Fatalf("presets = %+v", first["presets"])
	}
	ids := make(map[string]bool, len(presets))
	for _, raw := range presets {
		preset, _ := raw.(map[string]any)
		jobID, _ := preset["job_id"].(string)
		if jobID == "" || preset["stored"] != true || preset["error"] != nil {
			t.Fatalf("preset = %+v", preset)
		}
		ids[jobID] = true
	}
	if len(ids) != 4 {
		t.Fatalf("job ids did not differ: %+v", ids)
	}

	resp = prewarmRequest(t, srv, session.Token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d", resp.StatusCode)
	}
	second := decode(t, resp)
	for _, raw := range second["presets"].([]any) {
		preset, _ := raw.(map[string]any)
		if preset["stored"] != true || !ids[preset["job_id"].(string)] {
			t.Fatalf("preset was not reused: %+v", preset)
		}
	}

	resp = prewarmRequest(t, srv, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
