package store

import (
	"context"
	"database/sql"
	"errors"
)

// SetPosition records where a reader is in a book.
func (s *Store) SetPosition(ctx context.Context, userID, bookID int64, ord int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO positions (user_id, book_id, ord) VALUES (?, ?, ?)
		ON CONFLICT(user_id, book_id) DO UPDATE SET ord = excluded.ord, updated_at = CURRENT_TIMESTAMP`,
		userID, bookID, ord)
	return err
}

// GetPosition returns ok=false if the reader hasn't set a position.
func (s *Store) GetPosition(ctx context.Context, userID, bookID int64) (int, bool, error) {
	var ord int
	err := s.db.QueryRowContext(ctx, "SELECT ord FROM positions WHERE user_id = ? AND book_id = ?", userID, bookID).Scan(&ord)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ord, err == nil, err
}

// MarkFinished records that a reader finished a book. Only an explicit
// action from the reader should call this.
func (s *Store) MarkFinished(ctx context.Context, userID, bookID int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO finished_books (user_id, book_id) VALUES (?, ?) ON CONFLICT DO NOTHING", userID, bookID)
	return err
}
