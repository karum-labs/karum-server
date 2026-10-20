package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/config"
)

type storedAccount struct {
	account auth.Account
	hash    string
}

type authTestStore struct {
	accounts map[string]storedAccount
	byID     map[string]string
	sessions map[string]string
	linked   []string
}

func newAuthTestStore() *authTestStore {
	store := &authTestStore{
		accounts: make(map[string]storedAccount),
		byID:     make(map[string]string),
		sessions: make(map[string]string),
	}
	account := auth.Account{
		ID:          "acct-judge",
		Email:       "judge@karum.demo",
		DisplayName: "Karum Judge",
		IsJudge:     true,
	}
	store.accounts[account.Email] = storedAccount{account: account, hash: auth.HashPassword("secret")}
	store.byID[account.ID] = account.Email
	return store
}

func (s *authTestStore) FindAccountByEmail(_ context.Context, email string) (auth.Account, string, error) {
	row, ok := s.accounts[email]
	if !ok {
		return auth.Account{}, "", auth.ErrUnauthorized
	}
	return row.account, row.hash, nil
}

func (s *authTestStore) FindJudge(_ context.Context) (auth.Account, error) {
	for _, row := range s.accounts {
		if row.account.IsJudge {
			return row.account, nil
		}
	}
	return auth.Account{}, auth.ErrNoJudge
}

func (s *authTestStore) UpsertJudge(_ context.Context, id, email, hash, displayName string) (auth.Account, error) {
	account := auth.Account{ID: id, Email: email, DisplayName: displayName, IsJudge: true}
	s.accounts[email] = storedAccount{account: account, hash: hash}
	s.byID[id] = email
	return account, nil
}

func (s *authTestStore) InsertAccount(_ context.Context, account auth.Account, hash string) (auth.Account, error) {
	if _, ok := s.accounts[account.Email]; ok {
		return auth.Account{}, auth.ErrEmailTaken
	}
	account.IsJudge = false
	s.accounts[account.Email] = storedAccount{account: account, hash: hash}
	s.byID[account.ID] = account.Email
	return account, nil
}

func (s *authTestStore) CreateSession(_ context.Context, tokenHash, accountID string, _ time.Time) error {
	s.sessions[tokenHash] = accountID
	return nil
}

func (s *authTestStore) FindSession(_ context.Context, tokenHash string) (auth.Account, error) {
	accountID, ok := s.sessions[tokenHash]
	if !ok {
		return auth.Account{}, auth.ErrUnauthorized
	}
	email, ok := s.byID[accountID]
	if !ok {
		return auth.Account{}, auth.ErrUnauthorized
	}
	return s.accounts[email].account, nil
}

func (s *authTestStore) DeleteSession(_ context.Context, tokenHash string) error {
	delete(s.sessions, tokenHash)
	return nil
}

func (s *authTestStore) LinkCampaigns(_ context.Context, _ string, ids []string) error {
	s.linked = ids
	return nil
}

func authRouter(t *testing.T, hackathon bool) (http.Handler, *auth.Service) {
	t.Helper()
	store := newAuthTestStore()
	service := auth.NewService(store, time.Hour)
	router := NewRouterWithDeps(&config.Config{
		Mode:          config.ModeFixture,
		Version:       "test",
		HackathonMode: hackathon,
	}, Dependencies{Auth: service})
	return router, service
}

func decodeSession(t *testing.T, body []byte) auth.Session {
	t.Helper()
	var session auth.Session
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return session
}

func TestHealthReportsHackathonMode(t *testing.T) {
	router, _ := authRouter(t, true)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	var got HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !got.HackathonMode {
		t.Fatal("hackathon_mode = false, want true")
	}
}

func TestJudgeAuthFlow(t *testing.T) {
	router, _ := authRouter(t, true)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/judge", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("judge sign-in status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	session := decodeSession(t, w.Body.Bytes())
	if session.Token == "" || session.Account.ID != "acct-judge" {
		t.Fatalf("session = %+v, want judge token", session)
	}

	// The account endpoints require the token; the demo routes do not.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /auth/me status = %d, want 401", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated /auth/me status = %d, want 200", w.Code)
	}
	var me struct {
		Account auth.Account `json:"account"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Account.ID != "acct-judge" {
		t.Fatalf("me.Account = %+v, want judge", me.Account)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout /auth/me status = %d, want 401", w.Code)
	}
}

func TestJudgeRouteDisabledOutsideHackathonMode(t *testing.T) {
	router, _ := authRouter(t, false)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/judge", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("judge sign-in status = %d, want 404", w.Code)
	}
}

func TestPasswordLoginAndInvalidCredentials(t *testing.T) {
	router, _ := authRouter(t, true)

	body := `{"email":"judge@karum.demo","password":"secret"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if session := decodeSession(t, w.Body.Bytes()); session.Token == "" {
		t.Fatal("login returned an empty token")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"email":"judge@karum.demo","password":"wrong"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password status = %d, want 401", w.Code)
	}
}

func TestDemoRoutesStayAnonymousAndRejectBadTokens(t *testing.T) {
	router, _ := authRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scout", strings.NewReader(`{}`))
	router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatal("anonymous scout was blocked by auth; the public demo must stay open")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/scout", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token scout status = %d, want 401", w.Code)
	}
}

func TestAuthEndpointsReportUnconfiguredStore(t *testing.T) {
	router := NewRouterWithDeps(&config.Config{
		Mode:          config.ModeFixture,
		HackathonMode: true,
	}, Dependencies{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/judge", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("judge status = %d, want 503", w.Code)
	}
}

func TestRegisterEmailAndPassword(t *testing.T) {
	router, _ := authRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{
		"email":"owner@example.com",
		"password":"password1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (%s)", w.Code, w.Body.String())
	}
	session := decodeSession(t, w.Body.Bytes())
	if session.Token == "" || session.Account.Email != "owner@example.com" || session.Account.IsJudge {
		t.Fatalf("session = %+v", session)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{
		"email":"owner@example.com",
		"password":"password1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409 (%s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{
		"email":"owner@example.com",
		"password":"password1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

func TestAuthErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{auth.ErrInvalidCredentials, http.StatusUnauthorized},
		{auth.ErrUnauthorized, http.StatusUnauthorized},
		{auth.ErrNoJudge, http.StatusServiceUnavailable},
		{auth.ErrNotConfigured, http.StatusServiceUnavailable},
		{errors.New("database down"), http.StatusServiceUnavailable},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		writeAuthError(c, test.err)
		if w.Code != test.want {
			t.Errorf("writeAuthError(%v) status = %d, want %d", test.err, w.Code, test.want)
		}
	}
}
