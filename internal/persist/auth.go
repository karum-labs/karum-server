package persist

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/karum-labs/karum-server/internal/auth"
)

// FindAccountByEmail returns the public account and its password hash.
func (s *Store) FindAccountByEmail(ctx context.Context, email string) (auth.Account, string, error) {
	var account auth.Account
	var hash string
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, is_judge, password_hash
		FROM accounts
		WHERE email = $1
	`, email).Scan(&account.ID, &account.Email, &account.DisplayName, &account.IsJudge, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, "", auth.ErrUnauthorized
	}
	if err != nil {
		return auth.Account{}, "", err
	}
	return account, hash, nil
}

// FindJudge returns the seeded judge account.
func (s *Store) FindJudge(ctx context.Context) (auth.Account, error) {
	var account auth.Account
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, is_judge
		FROM accounts
		WHERE is_judge = true
		ORDER BY created_at
		LIMIT 1
	`).Scan(&account.ID, &account.Email, &account.DisplayName, &account.IsJudge)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, auth.ErrNoJudge
	}
	if err != nil {
		return auth.Account{}, err
	}
	return account, nil
}

// UpsertJudge creates or refreshes the one judge account.
func (s *Store) UpsertJudge(ctx context.Context, id, email, passwordHash, displayName string) (auth.Account, error) {
	var account auth.Account
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, email, password_hash, display_name, is_judge)
		VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email,
			password_hash = EXCLUDED.password_hash,
			display_name = EXCLUDED.display_name,
			is_judge = true
		RETURNING id, email, display_name, is_judge
	`, id, email, passwordHash, displayName).Scan(
		&account.ID, &account.Email, &account.DisplayName, &account.IsJudge,
	)
	return account, err
}

// InsertAccount creates a non-judge account. A duplicate email is ErrEmailTaken.
func (s *Store) InsertAccount(ctx context.Context, account auth.Account, passwordHash string) (auth.Account, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, email, password_hash, display_name, is_judge)
		VALUES ($1, $2, $3, $4, false)
		RETURNING id, email, display_name, is_judge
	`, account.ID, account.Email, passwordHash, account.DisplayName).Scan(
		&account.ID, &account.Email, &account.DisplayName, &account.IsJudge,
	)
	if isUnique(err) {
		return auth.Account{}, auth.ErrEmailTaken
	}
	return account, err
}

// LinkCampaigns points the four preset campaigns at the judge account.
func (s *Store) LinkCampaigns(ctx context.Context, accountID string, campaignIDs []string) error {
	if accountID == "" || len(campaignIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE campaigns
		SET account_id = $1
		WHERE id = ANY($2)
	`, accountID, campaignIDs)
	return err
}

// CreateSession stores the SHA-256 hash of an opaque token.
func (s *Store) CreateSession(ctx context.Context, tokenHash, accountID string, expiresAt time.Time) error {
	_, _ = s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, account_id, expires_at)
		VALUES ($1, $2, $3)
	`, tokenHash, accountID, expiresAt)
	return err
}

// FindSession returns the account for a live session.
func (s *Store) FindSession(ctx context.Context, tokenHash string) (auth.Account, error) {
	var account auth.Account
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.email, a.display_name, a.is_judge
		FROM sessions s
		JOIN accounts a ON a.id = s.account_id
		WHERE s.token_hash = $1 AND s.expires_at > now()
	`, tokenHash).Scan(&account.ID, &account.Email, &account.DisplayName, &account.IsJudge)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, auth.ErrUnauthorized
	}
	if err != nil {
		return auth.Account{}, err
	}
	return account, nil
}

// DeleteSession revokes one token.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteExpiredSessions is called once at startup.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

// Ping is the live Postgres probe used by GET /health.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("postgres is not open")
	}
	return s.pool.Ping(ctx)
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
