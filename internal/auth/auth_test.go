package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memAccount struct {
	account Account
	hash    string
}

type memStore struct {
	accounts  map[string]memAccount
	sessions  map[string]memSession
	judgeID   string
	linked    []string
	linkCalls int
}

type memSession struct {
	accountID string
	expires   time.Time
}

func newMemStore() *memStore {
	return &memStore{
		accounts: make(map[string]memAccount),
		sessions: make(map[string]memSession),
	}
}

func (m *memStore) FindAccountByEmail(_ context.Context, email string) (Account, string, error) {
	row, ok := m.accounts[email]
	if !ok {
		return Account{}, "", ErrUnauthorized
	}
	return row.account, row.hash, nil
}

func (m *memStore) FindJudge(_ context.Context) (Account, error) {
	for _, row := range m.accounts {
		if row.account.IsJudge {
			return row.account, nil
		}
	}
	return Account{}, ErrNoJudge
}

func (m *memStore) UpsertJudge(_ context.Context, id, email, hash, displayName string) (Account, error) {
	account := Account{ID: id, Email: email, DisplayName: displayName, IsJudge: true}
	m.accounts[email] = memAccount{account: account, hash: hash}
	m.judgeID = id
	return account, nil
}

func (m *memStore) InsertAccount(_ context.Context, account Account, hash string) (Account, error) {
	if _, ok := m.accounts[account.Email]; ok {
		return Account{}, ErrEmailTaken
	}
	account.IsJudge = false
	m.accounts[account.Email] = memAccount{account: account, hash: hash}
	return account, nil
}

func (m *memStore) CreateSession(_ context.Context, tokenHash, accountID string, expiresAt time.Time) error {
	m.sessions[tokenHash] = memSession{accountID: accountID, expires: expiresAt}
	return nil
}

func (m *memStore) FindSession(_ context.Context, tokenHash string) (Account, error) {
	session, ok := m.sessions[tokenHash]
	if !ok || session.expires.Before(time.Now()) {
		return Account{}, ErrUnauthorized
	}
	for _, row := range m.accounts {
		if row.account.ID == session.accountID {
			return row.account, nil
		}
	}
	return Account{}, ErrUnauthorized
}

func (m *memStore) DeleteSession(_ context.Context, tokenHash string) error {
	delete(m.sessions, tokenHash)
	return nil
}

func (m *memStore) LinkCampaigns(_ context.Context, _ string, ids []string) error {
	m.linkCalls++
	m.linked = append([]string(nil), ids...)
	return nil
}

func TestEnsureJudgeIsIdempotentAndLinksCampaigns(t *testing.T) {
	store := newMemStore()
	service := NewService(store, time.Hour)
	ids := []string{"cmp-a", "cmp-b", "cmp-c", "cmp-d"}

	first, err := service.EnsureJudge(context.Background(), "acct-judge", "Judge@Karum.Demo", "secret", "Judge", ids)
	if err != nil {
		t.Fatalf("EnsureJudge() error = %v", err)
	}
	if first.ID != "acct-judge" || !first.IsJudge {
		t.Fatalf("account = %+v, want acct-judge with judge flag", first)
	}
	if store.linkCalls != 1 || len(store.linked) != len(ids) {
		t.Fatalf("linked = %v after %d calls, want %d ids", store.linked, store.linkCalls, len(ids))
	}
	firstHash := store.accounts["judge@karum.demo"].hash
	if firstHash == "secret" || !strings.HasPrefix(firstHash, "$2") {
		t.Fatalf("stored hash = %q, want a bcrypt hash", firstHash)
	}

	second, err := service.EnsureJudge(context.Background(), "acct-judge", "judge@karum.demo", "secret", "Judge", ids)
	if err != nil {
		t.Fatalf("second EnsureJudge() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second id = %q, want %q", second.ID, first.ID)
	}
	if got := store.accounts["judge@karum.demo"].hash; got != firstHash {
		t.Fatal("restarting with an unchanged password rotated the bcrypt hash")
	}
}

func TestLoginAuthenticateAndLogout(t *testing.T) {
	store := newMemStore()
	service := NewService(store, time.Hour)
	if _, err := service.EnsureJudge(context.Background(), "acct-judge", "judge@karum.demo", "secret", "Judge", nil); err != nil {
		t.Fatalf("seed judge: %v", err)
	}

	session, err := service.Login(context.Background(), " JUDGE@karum.demo ", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Token == "" || session.ExpiresAt.Before(time.Now()) {
		t.Fatalf("session = %+v, want token and future expiry", session)
	}
	if session.Account.ID != "acct-judge" {
		t.Fatalf("session account = %+v, want judge", session.Account)
	}

	account, err := service.Authenticate(context.Background(), session.Token)
	if err != nil || account.ID != "acct-judge" {
		t.Fatalf("Authenticate() = %+v, %v; want judge", account, err)
	}

	if err := service.Logout(context.Background(), session.Token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), session.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate() after logout = %v, want ErrUnauthorized", err)
	}
}

func TestLoginRejectsUnknownEmailAndWrongPassword(t *testing.T) {
	store := newMemStore()
	service := NewService(store, time.Hour)
	if _, err := service.EnsureJudge(context.Background(), "acct-judge", "judge@karum.demo", "secret", "Judge", nil); err != nil {
		t.Fatalf("seed judge: %v", err)
	}

	for _, test := range []struct{ email, password string }{
		{"nobody@karum.demo", "secret"},
		{"judge@karum.demo", "wrong"},
	} {
		if _, err := service.Login(context.Background(), test.email, test.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q, %q) error = %v, want ErrInvalidCredentials", test.email, test.password, err)
		}
	}
}

func TestJudgeLoginNeedsASeededJudge(t *testing.T) {
	service := NewService(newMemStore(), time.Hour)
	if _, err := service.JudgeLogin(context.Background()); !errors.Is(err, ErrNoJudge) {
		t.Fatalf("JudgeLogin() error = %v, want ErrNoJudge", err)
	}

	store := newMemStore()
	service = NewService(store, time.Hour)
	if _, err := service.EnsureJudge(context.Background(), "acct-judge", "judge@karum.demo", "secret", "Judge", nil); err != nil {
		t.Fatalf("seed judge: %v", err)
	}
	session, err := service.JudgeLogin(context.Background())
	if err != nil || session.Account.ID != "acct-judge" {
		t.Fatalf("JudgeLogin() = %+v, %v; want judge", session, err)
	}
}

func TestRegisterSignsInAndRejectsADuplicateEmail(t *testing.T) {
	service := NewService(newMemStore(), time.Hour)

	session, err := service.Register(context.Background(), "Owner@Example.com", "password1")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if session.Token == "" || session.Account.Email != "owner@example.com" || session.Account.IsJudge {
		t.Fatalf("session = %+v", session)
	}
	if session.Account.DisplayName != "owner" {
		t.Fatalf("display name = %q, want owner", session.Account.DisplayName)
	}
	if _, err := service.Register(context.Background(), "owner@example.com", "password1"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate Register() error = %v, want ErrEmailTaken", err)
	}
	signedIn, err := service.Login(context.Background(), "owner@example.com", "password1")
	if err != nil || signedIn.Account.ID != session.Account.ID {
		t.Fatalf("Login() = %+v, %v", signedIn, err)
	}
	if _, err := service.Register(context.Background(), "short@example.com", "tiny"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("short password error = %v, want ErrInvalidRequest", err)
	}
}

func TestBearerToken(t *testing.T) {
	for _, test := range []struct{ header, want string }{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"Basic abc", ""},
		{"Bearer", ""},
		{"", ""},
	} {
		if got := BearerToken(test.header); got != test.want {
			t.Errorf("BearerToken(%q) = %q, want %q", test.header, got, test.want)
		}
	}
}
