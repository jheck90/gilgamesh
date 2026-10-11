package store

import (
	"context"
	"database/sql"
	"errors"
)

type Book struct {
	ID          int64
	SeriesID    int64
	SeriesName  string
	SeriesIndex float64
	Title       string
	Author      string
	SourcePath  string
	SHA256      string
}

type Chapter struct {
	Idx      int // 1-based
	Title    string
	FirstOrd int
	LastOrd  int
}

type Sentence struct {
	Ord        int
	ChapterIdx int
	Para       int
	Text       string
}

// CreateBook stores a parsed book and sets up its extraction progress.
func (s *Store) CreateBook(ctx context.Context, b Book, chapters []Chapter, sentences []Sentence, totalChunks, chunkWords int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		"INSERT INTO books (series_id, series_index, title, author, source_path, sha256) VALUES (?, ?, ?, ?, ?, ?)",
		b.SeriesID, b.SeriesIndex, b.Title, b.Author, b.SourcePath, b.SHA256)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, c := range chapters {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO chapters (book_id, idx, title, first_ord, last_ord) VALUES (?, ?, ?, ?, ?)",
			id, c.Idx, c.Title, c.FirstOrd, c.LastOrd); err != nil {
			return 0, err
		}
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO sentences (book_id, ord, chapter_idx, para, text) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, se := range sentences {
		if _, err := stmt.ExecContext(ctx, id, se.Ord, se.ChapterIdx, se.Para, se.Text); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO ingest_progress (book_id, total_chunks, chunk_words) VALUES (?, ?, ?)",
		id, totalChunks, chunkWords); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

const bookCols = `b.id, b.series_id, s.name, b.series_index, b.title, b.author, b.source_path, b.sha256`

func scanBook(sc interface{ Scan(...any) error }) (Book, error) {
	var b Book
	err := sc.Scan(&b.ID, &b.SeriesID, &b.SeriesName, &b.SeriesIndex, &b.Title, &b.Author, &b.SourcePath, &b.SHA256)
	return b, err
}

func (s *Store) bookWhere(ctx context.Context, where string, args ...any) (Book, bool, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+bookCols+" FROM books b JOIN series s ON s.id = b.series_id WHERE "+where, args...)
	b, err := scanBook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, false, nil
	}
	return b, err == nil, err
}

func (s *Store) BookBySHA(ctx context.Context, sha string) (Book, bool, error) {
	return s.bookWhere(ctx, "b.sha256 = ?", sha)
}

func (s *Store) BookByID(ctx context.Context, id int64) (Book, bool, error) {
	return s.bookWhere(ctx, "b.id = ?", id)
}

// Books lists every book, grouped by series in reading order.
func (s *Store) Books(ctx context.Context) ([]Book, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+bookCols+" FROM books b JOIN series s ON s.id = b.series_id ORDER BY s.name, b.series_index, b.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LaterInSeries lists books already stored that come after index in the
// series.
func (s *Store) LaterInSeries(ctx context.Context, seriesID int64, index float64) ([]Book, error) {
	all, err := s.Books(ctx)
	if err != nil {
		return nil, err
	}
	var out []Book
	for _, b := range all {
		if b.SeriesID == seriesID && b.SeriesIndex > index {
			out = append(out, b)
		}
	}
	return out, nil
}

// DeleteBook removes a book with everything extracted from it, including
// entities first met in it.
func (s *Store) DeleteBook(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM books WHERE id = ?", id)
	return err
}

func (s *Store) Chapters(ctx context.Context, bookID int64) ([]Chapter, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT idx, title, first_ord, last_ord FROM chapters WHERE book_id = ? ORDER BY idx", bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chapter
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.Idx, &c.Title, &c.FirstOrd, &c.LastOrd); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Sentences(ctx context.Context, bookID int64) ([]Sentence, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT ord, chapter_idx, para, text FROM sentences WHERE book_id = ? ORDER BY ord", bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sentence
	for rows.Next() {
		var se Sentence
		if err := rows.Scan(&se.Ord, &se.ChapterIdx, &se.Para, &se.Text); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

func (s *Store) Sentence(ctx context.Context, bookID int64, ord int) (Sentence, error) {
	se := Sentence{Ord: ord}
	err := s.db.QueryRowContext(ctx, "SELECT chapter_idx, para, text FROM sentences WHERE book_id = ? AND ord = ?", bookID, ord).
		Scan(&se.ChapterIdx, &se.Para, &se.Text)
	return se, err
}

type Progress struct {
	NextChunk   int
	TotalChunks int
	ChunkWords  int
}

func (p Progress) Done() bool { return p.NextChunk >= p.TotalChunks }

func (s *Store) Progress(ctx context.Context, bookID int64) (Progress, error) {
	var p Progress
	err := s.db.QueryRowContext(ctx, "SELECT next_chunk, total_chunks, chunk_words FROM ingest_progress WHERE book_id = ?", bookID).
		Scan(&p.NextChunk, &p.TotalChunks, &p.ChunkWords)
	return p, err
}
