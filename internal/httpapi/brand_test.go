package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountBrandProfileRoundTrip(t *testing.T) {
	router, _ := authRouter(t, true)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/judge", nil))
	token := decodeSession(t, w.Body.Bytes()).Token

	send := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/account/brand", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if got := send(http.MethodGet, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"saved":false`) {
		t.Fatalf("empty profile = %d %s", got.Code, got.Body.String())
	}
	if got := send(http.MethodPut, `{"name":" "}`); got.Code != http.StatusBadRequest {
		t.Fatalf("nameless profile = %d, want 400", got.Code)
	}
	put := send(http.MethodPut, `{"name":"Bloom Soda Co.","category":"Craft soft drinks","skus":[{"name":"Smoked Cola"},{"name":""}]}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d %s", put.Code, put.Body.String())
	}
	var out struct {
		Brand brandProfileBody `json:"brand"`
		Saved bool             `json:"saved"`
	}
	if err := json.Unmarshal(send(http.MethodGet, "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Saved || out.Brand.Category != "Craft soft drinks" || len(out.Brand.SKUs) != 1 || out.Brand.SKUs[0].ID == "" {
		t.Fatalf("stored profile = %+v", out)
	}

	token = ""
	if got := send(http.MethodGet, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read = %d, want 401", got.Code)
	}
}
