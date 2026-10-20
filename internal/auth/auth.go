// Package auth implements the gateway's small first-party account layer:
// email/password registration, bcrypt password hashes, opaque server-side
// sessions, and one preseeded judge account.
//
// This is deliberately not a login wall. The public demo keeps working
// anonymously; a session only attributes a run to an account. HACKATHON_MODE
// enables the passwordless judge sign-in the login banner calls. The banner
// itself is a client build flag.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	minPasswordLength = 8
	maxPasswordBytes  = 72
)

// ErrInvalidCredentials is returned for an unknown email and a wrong password
// alike, so the API never reveals which one was wrong.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrUnauthorized means the session token is missing, expired, or unknown.
var ErrUnauthorized = errors.New("unauthorized")

// ErrNotConfigured means no account store is attached to this deployment.
var ErrNotConfigured = errors.New("auth is not configured")

// ErrNoJudge means the judged account has not been seeded.
var ErrNoJudge = errors.New("judge account is not configured")

// ErrEmailTaken means registration collided with an existing email.
var ErrEmailTaken = errors.New("email taken")

// ErrInvalidRequest means the caller sent a field the API can safely describe.
var ErrInvalidRequest = errors.New("invalid request")

// ValidationError is a request problem whose message is safe to show.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func (e *ValidationError) Unwrap() error { return ErrInvalidRequest }

// Account is the public account row returned to the client.
type Account struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	IsJudge     bool   `json:"is_judge"`
}

// Session is one successful sign-in.
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Account   Account   `json:"account"`
}

// Store is the Postgres surface auth needs. internal/persist implements it.
type Store interface {
	FindAccountByEmail(ctx context.Context, email string) (Account, string, error)
	FindJudge(ctx context.Context) (Account, error)
	UpsertJudge(ctx context.Context, id, email, passwordHash, displayName string) (Account, error)
	InsertAccount(ctx context.Context, account Account, passwordHash string) (Account, error)
	CreateSession(ctx context.Context, tokenHash, accountID string, expiresAt time.Time) error
	FindSession(ctx context.Context, tokenHash string) (Account, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	LinkCampaigns(ctx context.Context, accountID string, campaignIDs []string) error
}

// Service owns password checking, token minting, and session lookup.
type Service struct {
	store     Store
	ttl       time.Duration
	now       func() time.Time
	dummyHash []byte
}

// NewService returns a service with a seven-day default session.
func NewService(store Store, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	dummy, _ := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), 12)
	return &Service{store: store, ttl: ttl, now: time.Now, dummyHash: dummy}
}

// Enabled reports whether an account store is attached.
func (s *Service) Enabled() bool { return s != nil && s.store != nil }

// EnsureJudge creates or updates the one judge account and links the preset
// campaigns to it. It is idempotent: an unchanged password keeps its hash, so
// restarting the gateway does not rotate anything.
func (s *Service) EnsureJudge(ctx context.Context, id, email, password, displayName string, campaignIDs []string) (Account, error) {
	if !s.Enabled() {
		return Account{}, ErrNotConfigured
	}
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return Account{}, ErrNotConfigured
	}
	if id == "" {
		id = "acct-judge"
	}
	if displayName == "" {
		displayName = "Karum Judge"
	}

	account, existing, err := s.store.FindAccountByEmail(ctx, email)
	if err == nil {
		id = account.ID
		if bcrypt.CompareHashAndPassword([]byte(existing), []byte(password)) == nil {
			account, err = s.store.UpsertJudge(ctx, id, email, existing, displayName)
		} else {
			account, err = s.store.UpsertJudge(ctx, id, email, hashPassword(password), displayName)
		}
	} else if errors.Is(err, ErrUnauthorized) || isNotFound(err) {
		account, err = s.store.UpsertJudge(ctx, id, email, hashPassword(password), displayName)
	}
	if err != nil {
		return Account{}, err
	}
	if err := s.store.LinkCampaigns(ctx, account.ID, campaignIDs); err != nil {
		return Account{}, err
	}
	return account, nil
}

// Register creates an account from an email and password and signs it in.
func (s *Service) Register(ctx context.Context, email, password string) (Session, error) {
	if !s.Enabled() {
		return Session{}, ErrNotConfigured
	}
	email = normalizeEmail(email)
	if err := validateIdentity(email, password); err != nil {
		return Session{}, err
	}
	if _, _, err := s.store.FindAccountByEmail(ctx, email); err == nil {
		return Session{}, ErrEmailTaken
	} else if !errors.Is(err, ErrUnauthorized) && !isNotFound(err) {
		return Session{}, err
	}
	id, err := newID("acct")
	if err != nil {
		return Session{}, err
	}
	account, err := s.store.InsertAccount(ctx, Account{
		ID:          id,
		Email:       email,
		DisplayName: displayNameFromEmail(email),
		IsJudge:     false,
	}, hashPassword(password))
	if err != nil {
		return Session{}, err
	}
	return s.newSession(ctx, account)
}

// Login checks an email/password pair and creates a session.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	if !s.Enabled() {
		return Session{}, ErrNotConfigured
	}
	account, hash, err := s.store.FindAccountByEmail(ctx, normalizeEmail(email))
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return Session{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Session{}, ErrInvalidCredentials
	}
	return s.newSession(ctx, account)
}

// JudgeLogin signs into the preseeded judge account without a password. The
// HTTP layer gates this route on HACKATHON_MODE.
func (s *Service) JudgeLogin(ctx context.Context) (Session, error) {
	if !s.Enabled() {
		return Session{}, ErrNotConfigured
	}
	account, err := s.store.FindJudge(ctx)
	if err != nil {
		return Session{}, ErrNoJudge
	}
	return s.newSession(ctx, account)
}

// Authenticate resolves a raw bearer token to an account.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if !s.Enabled() {
		return Account{}, ErrNotConfigured
	}
	if strings.TrimSpace(token) == "" {
		return Account{}, ErrUnauthorized
	}
	account, err := s.store.FindSession(ctx, HashToken(token))
	if err != nil {
		return Account{}, ErrUnauthorized
	}
	return account, nil
}

// Logout revokes one session. An already-expired token is a no-op.
func (s *Service) Logout(ctx context.Context, token string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	if strings.TrimSpace(token) == "" {
		return ErrUnauthorized
	}
	return s.store.DeleteSession(ctx, HashToken(token))
}

func (s *Service) newSession(ctx context.Context, account Account) (Session, error) {
	token, err := NewToken()
	if err != nil {
		return Session{}, err
	}
	expires := s.now().UTC().Add(s.ttl)
	if err := s.store.CreateSession(ctx, HashToken(token), account.ID, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires, Account: account}, nil
}

func validateIdentity(email, password string) error {
	if !validEmail(email) {
		return &ValidationError{Message: "enter a valid email address"}
	}
	if len(password) < minPasswordLength || len(password) > maxPasswordBytes {
		return &ValidationError{Message: "password must be 8 to 72 characters"}
	}
	return nil
}

func validEmail(email string) bool {
	if len(email) < 3 || len(email) > 320 {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at >= len(email)-1 {
		return false
	}
	domain := email[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

func displayNameFromEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return email
	}
	return email[:at]
}

// NewToken returns 32 random bytes as an unpadded base64url token.
func NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken is the one-way key stored in the sessions table.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashPassword is exported for tests and the rare migration script.
func HashPassword(password string) string { return hashPassword(password) }

func hashPassword(password string) string {
	raw, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return ""
	}
	return string(raw)
}

func newID(prefix string) (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(raw), nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// isNotFound keeps persist's pgx.ErrNoRows out of this package's API without
// importing pgx. Persist wraps missing rows with ErrUnauthorized.
func isNotFound(err error) bool {
	return err != nil && (errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "no rows"))
}
