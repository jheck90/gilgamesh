package store

import (
	"context"
	"database/sql"
	"errors"
)

const addQueueTable = `CREATE TABLE add_queue (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path         TEXT    NOT NULL,
  sha256       TEXT    NOT NULL,
  series       TEXT    NOT NULL,
  series_index REAL    NOT NULL,
  skip         TEXT    NOT NULL DEFAULT '',
  chunk_words  INTEGER NOT NULL,
  model        TEXT    NOT NULL DEFAULT '',
  effort       TEXT    NOT NULL DEFAULT '',
  then_edit    INTEGER NOT NULL DEFAULT 0,
  state        TEXT    NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'adding', 'editing', 'paused', 'done')),
  reason       TEXT    NOT NULL DEFAULT '',
  book_id      INTEGER REFERENCES books(id) ON DELETE SET NULL,
  created_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

const (
	AddWaiting = "waiting"
	AddAdding  = "adding"
	AddEditing = "editing"
	AddPaused  = "paused"
	AddDone    = "done"
)

// QueuedAdd is a book waiting to be added, or being added, in the
// background.
type QueuedAdd struct {
	ID          int64
	UserID      int64 // who started it; it runs on their key
	UserName    string
	Path        string
	SHA256      string
	Series      string
	SeriesIndex float64
	Skip        string
	ChunkWords  int
	Model       string
	Effort      string
	ThenEdit    bool
	State       string
	Reason      string
	BookID      int64 // 0 until the book is stored
	// Progress of reading the book; zero until it's stored.
	Progress Progress
}

// QueueAdd puts a in the queue, waiting.
func (s *Store) QueueAdd(ctx context.Context, a QueuedAdd) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO add_queue (user_id, path, sha256, series, series_index, skip, chunk_words, model, effort, then_edit)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.Path, a.SHA256, a.Series, a.SeriesIndex, a.Skip, a.ChunkWords, a.Model, a.Effort, a.ThenEdit)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const addCols = `q.id, q.user_id, u.name, q.path, q.sha256, q.series, q.series_index, q.skip, q.chunk_words, q.model, q.effort,
	q.then_edit, q.state, q.reason, COALESCE(q.book_id, 0), COALESCE(p.next_chunk, 0), COALESCE(p.total_chunks, 0), COALESCE(p.chunk_words, 0)
	FROM add_queue q JOIN users u ON u.id = q.user_id LEFT JOIN ingest_progress p ON p.book_id = q.book_id`

func (s *Store) adds(ctx context.Context, where, limit string, args ...any) ([]QueuedAdd, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+addCols+" WHERE "+where+" ORDER BY q.id"+limit, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedAdd
	for rows.Next() {
		var a QueuedAdd
		if err := rows.Scan(&a.ID, &a.UserID, &a.UserName, &a.Path, &a.SHA256, &a.Series, &a.SeriesIndex, &a.Skip, &a.ChunkWords,
			&a.Model, &a.Effort, &a.ThenEdit, &a.State, &a.Reason, &a.BookID,
			&a.Progress.NextChunk, &a.Progress.TotalChunks, &a.Progress.ChunkWords); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) addWhere(ctx context.Context, where string, args ...any) (QueuedAdd, bool, error) {
	as, err := s.adds(ctx, where, " LIMIT 1", args...)
	if err != nil || len(as) == 0 {
		return QueuedAdd{}, false, err
	}
	return as[0], true, nil
}

// Adds lists the whole queue, oldest first.
func (s *Store) Adds(ctx context.Context) ([]QueuedAdd, error) { return s.adds(ctx, "1", "") }

func (s *Store) QueuedAddByID(ctx context.Context, id int64) (QueuedAdd, bool, error) {
	return s.addWhere(ctx, "q.id = ?", id)
}

// NextAdd returns the oldest waiting entry.
func (s *Store) NextAdd(ctx context.Context) (QueuedAdd, bool, error) {
	return s.addWhere(ctx, "q.state = ?", AddWaiting)
}

// PendingAddBySHA returns an entry for the file that isn't done yet.
func (s *Store) PendingAddBySHA(ctx context.Context, sha string) (QueuedAdd, bool, error) {
	return s.addWhere(ctx, "q.sha256 = ? AND q.state <> ?", sha, AddDone)
}

// SetAddState moves an entry to state, with the reason when it's paused.
func (s *Store) SetAddState(ctx context.Context, id int64, state, reason string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE add_queue SET state = ?, reason = ? WHERE id = ?", state, reason, id)
	return err
}

func (s *Store) SetAddBook(ctx context.Context, id, bookID int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE add_queue SET book_id = ? WHERE id = ?", bookID, id)
	return err
}

func (s *Store) DeleteAdd(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM add_queue WHERE id = ?", id)
	return err
}

// ResumeInterruptedAdds puts back in line whatever was running when the
// server stopped, ahead of the rest since it's oldest.
func (s *Store) ResumeInterruptedAdds(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE add_queue SET state = ? WHERE state IN (?, ?)", AddWaiting, AddAdding, AddEditing)
	return err
}

// UserByID returns ok=false if there is no such user.
func (s *Store) UserByID(ctx context.Context, id int64) (User, bool, error) {
	u := User{ID: id}
	err := s.db.QueryRowContext(ctx, "SELECT name, is_admin FROM users WHERE id = ?", id).Scan(&u.Name, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}
