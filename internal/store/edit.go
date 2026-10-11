package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jheck90/gilgamesh/internal/text"
)

// Notes is everything noted from one book, with row ids, for an editor to
// review.
type Notes struct {
	Book     Book
	Chapters []Chapter
	Entities []NoteEntity // in order of first appearance
	Markers  []NoteMarker
}

type NoteEntity struct {
	ID          int64
	Name        string
	Type        string
	Narrator    bool
	FirstBookID int64
	FirstIndex  float64 // series index of the first book
	FirstOrd    int
	Aliases     []NoteAlias // from any book
	Facts       []NoteFact  // from this book only
	OtherFacts  int         // facts from other books
}

type NoteAlias struct {
	ID        int64
	Alias     string
	BookID    int64
	BookIndex float64 // series index of BookID
	Ord       int
}

type NoteFact struct {
	ID   int64
	Text string
	Ord  int
}

type NoteMarker struct {
	ID    int64
	Label string
	Ord   int
}

// BookNotes loads the entities first met in a book or given facts or names
// in it, with this book's facts, and the book's time markers.
func (s *Store) BookNotes(ctx context.Context, b Book) (Notes, error) {
	n := Notes{Book: b}
	var err error
	if n.Chapters, err = s.Chapters(ctx, b.ID); err != nil {
		return n, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.name, e.type, e.narrator, e.first_book_id, fb.series_index, e.first_ord
		FROM entities e JOIN books fb ON fb.id = e.first_book_id
		WHERE e.series_id = ? AND (e.first_book_id = ?
			OR e.id IN (SELECT entity_id FROM facts WHERE book_id = ?)
			OR e.id IN (SELECT entity_id FROM aliases WHERE book_id = ?))
		ORDER BY fb.series_index, e.first_ord, e.id`, b.SeriesID, b.ID, b.ID, b.ID)
	if err != nil {
		return n, err
	}
	idx := map[int64]int{}
	for rows.Next() {
		var e NoteEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Narrator, &e.FirstBookID, &e.FirstIndex, &e.FirstOrd); err != nil {
			rows.Close()
			return n, err
		}
		idx[e.ID] = len(n.Entities)
		n.Entities = append(n.Entities, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT a.id, a.entity_id, a.alias, a.book_id, ab.series_index, a.revealed_ord
		FROM aliases a JOIN entities e ON e.id = a.entity_id JOIN books ab ON ab.id = a.book_id
		WHERE e.series_id = ? ORDER BY ab.series_index, a.revealed_ord, a.id`, b.SeriesID)
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var a NoteAlias
		var eid int64
		if err := rows.Scan(&a.ID, &eid, &a.Alias, &a.BookID, &a.BookIndex, &a.Ord); err != nil {
			rows.Close()
			return n, err
		}
		if i, ok := idx[eid]; ok {
			n.Entities[i].Aliases = append(n.Entities[i].Aliases, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT f.id, f.entity_id, f.text, f.book_id, f.revealed_ord
		FROM facts f JOIN entities e ON e.id = f.entity_id WHERE e.series_id = ? ORDER BY f.revealed_ord, f.id`, b.SeriesID)
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var f NoteFact
		var eid, bid int64
		if err := rows.Scan(&f.ID, &eid, &f.Text, &bid, &f.Ord); err != nil {
			rows.Close()
			return n, err
		}
		i, ok := idx[eid]
		switch {
		case !ok:
		case bid == b.ID:
			n.Entities[i].Facts = append(n.Entities[i].Facts, f)
		default:
			n.Entities[i].OtherFacts++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}

	rows, err = s.db.QueryContext(ctx, "SELECT id, label, ord FROM time_markers WHERE book_id = ? ORDER BY ord, id", b.ID)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	for rows.Next() {
		var m NoteMarker
		if err := rows.Scan(&m.ID, &m.Label, &m.Ord); err != nil {
			return n, err
		}
		n.Markers = append(n.Markers, m)
	}
	return n, rows.Err()
}

// Edit tidies one book's notes. It can only remove and combine, never add
// text, so applying it can't put anything in front of a reader earlier
// than the book revealed it.
type Edit struct {
	BookID      int64
	SeriesID    int64
	CategoryMap map[string]string // old category -> existing category, series-wide
	Merges      []Merge
	Titles      []Title
	DropEntity  []int64
	DropAlias   []int64
	DropFact    []int64
	DropMarker  []int64
	Usage       *UsageRec
	// Model made the plan. Set it to record that the book was edited;
	// leave it empty to only record Usage.
	Model string
}

// Merge folds From into Into. Into keeps its own first appearance, and
// nothing from From is revealed earlier than it was: each From's name
// becomes an alias revealed where that name first appeared, and its facts
// and aliases are revealed no earlier than From was met. If Into is met
// later than From, From's notes just wait until Into appears.
type Merge struct {
	Into int64
	From []int64
}

// Title shows Entity by one of its own aliases from the point that alias
// is revealed.
type Title struct {
	Entity, Alias int64
}

type EditStats struct {
	Retyped, Merged, Titled, Entities, Aliases, Facts, Markers int
}

// ApplyEdit applies e in one transaction. Drops are limited to rows from
// e's book, so a mistaken id can't reach into another book.
func (s *Store) ApplyEdit(ctx context.Context, e Edit) (EditStats, error) {
	var st EditStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	exec := func(n *int, q string, args ...any) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		k, err := res.RowsAffected()
		*n += int(k)
		return err
	}
	for from, to := range e.CategoryMap {
		if err := exec(&st.Retyped, "UPDATE entities SET type = ? WHERE series_id = ? AND type = ?", to, e.SeriesID, from); err != nil {
			return st, err
		}
	}
	for _, m := range e.Merges {
		for _, from := range m.From {
			if err := merge(ctx, tx, e.SeriesID, e.BookID, m.Into, from); err != nil {
				return st, err
			}
			st.Merged++
		}
	}
	for _, t := range e.Titles {
		if err := exec(&st.Titled, `UPDATE entities SET title_alias_id = ? WHERE id = ? AND series_id = ?
			AND EXISTS (SELECT 1 FROM aliases WHERE id = ? AND entity_id = ?)`, t.Alias, t.Entity, e.SeriesID, t.Alias, t.Entity); err != nil {
			return st, err
		}
	}
	for _, id := range e.DropEntity {
		if err := exec(&st.Entities, "DELETE FROM entities WHERE id = ? AND first_book_id = ? AND narrator = 0", id, e.BookID); err != nil {
			return st, err
		}
	}
	for _, id := range e.DropAlias {
		if err := exec(&st.Aliases, "DELETE FROM aliases WHERE id = ? AND book_id = ?", id, e.BookID); err != nil {
			return st, err
		}
	}
	for _, id := range e.DropFact {
		if err := exec(&st.Facts, "DELETE FROM facts WHERE id = ? AND book_id = ?", id, e.BookID); err != nil {
			return st, err
		}
	}
	for _, id := range e.DropMarker {
		if err := exec(&st.Markers, "DELETE FROM time_markers WHERE id = ? AND book_id = ?", id, e.BookID); err != nil {
			return st, err
		}
	}
	if u := e.Usage; u != nil {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO usage (user_id, key_owner_id, book_id, provider, model, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)",
			u.UserID, u.KeyOwnerID, e.BookID, u.Provider, u.Model, u.InputTokens, u.OutputTokens); err != nil {
			return st, err
		}
	}
	if e.Model != "" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO edits (book_id, model) VALUES (?, ?)", e.BookID, e.Model); err != nil {
			return st, err
		}
	}
	return st, tx.Commit()
}

// LastEdit returns when a book was last edited, if ever.
func (s *Store) LastEdit(ctx context.Context, bookID int64) (string, bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, "SELECT created_at FROM edits WHERE book_id = ? ORDER BY id DESC LIMIT 1", bookID).Scan(&at)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return at, err == nil, err
}

func merge(ctx context.Context, tx *sql.Tx, seriesID, bookID, into, from int64) error {
	type ent struct {
		name     string
		narrator bool
		bookID   int64
		index    float64
		ord      int
	}
	get := func(id int64) (ent, error) {
		var e ent
		err := tx.QueryRowContext(ctx, `SELECT e.name, e.narrator, e.first_book_id, b.series_index, e.first_ord
			FROM entities e JOIN books b ON b.id = e.first_book_id WHERE e.id = ? AND e.series_id = ?`, id, seriesID).
			Scan(&e.name, &e.narrator, &e.bookID, &e.index, &e.ord)
		if err == sql.ErrNoRows {
			return e, fmt.Errorf("store: merge: no entity %d in this series", id)
		}
		return e, err
	}
	in, err := get(into)
	if err != nil {
		return err
	}
	fr, err := get(from)
	if err != nil {
		return err
	}
	if into == from || fr.bookID != bookID {
		return fmt.Errorf("store: merge: entity %d wasn't first met in this book", from)
	}

	// Names already on the kept entity, so nothing is listed twice.
	have := map[string]bool{text.Key(in.name): true}
	rows, err := tx.QueryContext(ctx, "SELECT alias FROM aliases WHERE entity_id = ?", into)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return err
		}
		have[text.Key(a)] = true
	}
	rows.Close()

	type alias struct {
		id    int64
		alias string
	}
	var moving []alias
	rows, err = tx.QueryContext(ctx, "SELECT id, alias FROM aliases WHERE entity_id = ? ORDER BY revealed_ord, id", from)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a alias
		if err := rows.Scan(&a.id, &a.alias); err != nil {
			rows.Close()
			return err
		}
		moving = append(moving, a)
	}
	rows.Close()
	for _, a := range moving {
		var err error
		if k := text.Key(a.alias); have[k] {
			_, err = tx.ExecContext(ctx, "DELETE FROM aliases WHERE id = ?", a.id)
		} else {
			have[k] = true
			_, err = tx.ExecContext(ctx, `UPDATE aliases SET entity_id = ?,
				revealed_ord = CASE WHEN book_id = ? AND revealed_ord < ? THEN ? ELSE revealed_ord END WHERE id = ?`,
				into, fr.bookID, fr.ord, fr.ord, a.id)
		}
		if err != nil {
			return err
		}
	}
	if !have[text.Key(fr.name)] {
		if _, err := tx.ExecContext(ctx, "INSERT INTO aliases (entity_id, alias, book_id, revealed_ord) VALUES (?, ?, ?, ?)",
			into, fr.name, fr.bookID, fr.ord); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM facts WHERE entity_id = ?
		AND lower(text) IN (SELECT lower(text) FROM facts WHERE entity_id = ?)`, from, into); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE facts SET entity_id = ?,
		revealed_ord = CASE WHEN book_id = ? AND revealed_ord < ? THEN ? ELSE revealed_ord END WHERE entity_id = ?`,
		into, fr.bookID, fr.ord, fr.ord, from); err != nil {
		return err
	}
	if fr.narrator && !in.narrator {
		if _, err := tx.ExecContext(ctx, "UPDATE entities SET narrator = 1 WHERE id = ?", into); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM entities WHERE id = ?", from)
	return err
}

// Backup writes a consistent copy of the database to path.
func (s *Store) Backup(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("store: backup needs a path")
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}
