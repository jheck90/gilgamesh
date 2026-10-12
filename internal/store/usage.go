package store

import "context"

// BookUsage is what one model spent on a book at one effort: reading all
// of it so far, or one edit of its notes. Words is the book's length.
type BookUsage struct {
	Effort        string
	Input, Output int64
	Words         int
}

// bookWords counts a book's words by its spaces; sentences are stored with
// single spaces between words.
const bookWords = `(SELECT COALESCE(SUM(LENGTH(text) - LENGTH(REPLACE(text, ' ', '')) + 1), 0)
  FROM sentences s WHERE s.book_id = u.book_id)`

// IngestUsage returns, for each book model has read and each effort it
// read at, the tokens it spent.
func (s *Store) IngestUsage(ctx context.Context, model string) ([]BookUsage, error) {
	return s.bookUsage(ctx, `SELECT u.effort, SUM(u.input_tokens), SUM(u.output_tokens), `+bookWords+`
		FROM usage u WHERE u.model = ? AND u.kind = 'ingest' AND u.book_id IS NOT NULL
		GROUP BY u.book_id, u.effort ORDER BY u.book_id, u.effort`, model)
}

// EditUsage returns the tokens of each edit model has made.
func (s *Store) EditUsage(ctx context.Context, model string) ([]BookUsage, error) {
	return s.bookUsage(ctx, `SELECT u.effort, u.input_tokens, u.output_tokens, `+bookWords+`
		FROM usage u WHERE u.model = ? AND u.kind = 'edit' AND u.book_id IS NOT NULL
		ORDER BY u.id`, model)
}

func (s *Store) bookUsage(ctx context.Context, q string, args ...any) ([]BookUsage, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookUsage
	for rows.Next() {
		var b BookUsage
		if err := rows.Scan(&b.Effort, &b.Input, &b.Output, &b.Words); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
