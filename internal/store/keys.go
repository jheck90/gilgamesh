package store

import (
	"context"
	"database/sql"
	"errors"
)

type APIKey struct {
	Provider string
	BaseURL  string
	Model    string
	Sealed   []byte
}

func (s *Store) SetAPIKey(ctx context.Context, userID int64, k APIKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (user_id, provider, base_url, model, sealed) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET provider = excluded.provider, base_url = excluded.base_url,
		model = excluded.model, sealed = excluded.sealed, created_at = CURRENT_TIMESTAMP`,
		userID, k.Provider, k.BaseURL, k.Model, k.Sealed)
	return err
}

func (s *Store) GetAPIKey(ctx context.Context, userID int64) (APIKey, bool, error) {
	var k APIKey
	err := s.db.QueryRowContext(ctx, "SELECT provider, base_url, model, sealed FROM api_keys WHERE user_id = ?", userID).
		Scan(&k.Provider, &k.BaseURL, &k.Model, &k.Sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, false, nil
	}
	return k, err == nil, err
}

func (s *Store) DeleteAPIKey(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE user_id = ?", userID)
	return err
}

// Admin returns the admin user (the first one created).
func (s *Store) Admin(ctx context.Context) (User, bool, error) {
	var u User
	err := s.db.QueryRowContext(ctx, "SELECT id, name, is_admin FROM users WHERE is_admin = 1 ORDER BY id LIMIT 1").
		Scan(&u.ID, &u.Name, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}

const (
	GrantRequested = "requested"
	GrantApproved  = "approved"
	GrantDenied    = "denied"
	GrantRevoked   = "revoked"
)

type Grant struct {
	UserID     int64
	UserName   string
	Status     string
	MonthlyCap int64 // tokens; 0 means no cap
	Note       string
}

// RequestGrant asks the admin for use of their key. An approved grant is
// left as it is.
func (s *Store) RequestGrant(ctx context.Context, userID int64, note string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO key_grants (user_id, status, note) VALUES (?, 'requested', ?)
		ON CONFLICT(user_id) DO UPDATE SET status = 'requested', note = excluded.note,
		requested_at = CURRENT_TIMESTAMP, decided_at = NULL, decided_by = NULL
		WHERE key_grants.status <> 'approved'`, userID, note)
	return err
}

// DecideGrant approves, denies or revokes a user's grant.
func (s *Store) DecideGrant(ctx context.Context, userID int64, status string, monthlyCap int64, adminID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE key_grants SET status = ?, monthly_token_cap = ?,
		decided_at = CURRENT_TIMESTAMP, decided_by = ? WHERE user_id = ?`, status, monthlyCap, adminID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("that user hasn't asked to use the admin key")
	}
	return nil
}

func (s *Store) GetGrant(ctx context.Context, userID int64) (Grant, bool, error) {
	g := Grant{UserID: userID}
	err := s.db.QueryRowContext(ctx, `SELECT u.name, g.status, g.monthly_token_cap, g.note
		FROM key_grants g JOIN users u ON u.id = g.user_id WHERE g.user_id = ?`, userID).
		Scan(&g.UserName, &g.Status, &g.MonthlyCap, &g.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, false, nil
	}
	return g, err == nil, err
}

func (s *Store) Grants(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.user_id, u.name, g.status, g.monthly_token_cap, g.note
		FROM key_grants g JOIN users u ON u.id = g.user_id ORDER BY g.requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.UserID, &g.UserName, &g.Status, &g.MonthlyCap, &g.Note); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// BorrowedTokensThisMonth totals the tokens a user has spent on other
// people's keys since the start of the current UTC month.
func (s *Store) BorrowedTokensThisMonth(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM usage
		WHERE user_id = ? AND key_owner_id <> user_id AND created_at >= strftime('%Y-%m-01 00:00:00', 'now')`, userID).Scan(&n)
	return n, err
}

// TokensThisMonth totals all tokens a user has spent this UTC month.
func (s *Store) TokensThisMonth(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM usage
		WHERE user_id = ? AND created_at >= strftime('%Y-%m-01 00:00:00', 'now')`, userID).Scan(&n)
	return n, err
}
