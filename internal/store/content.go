package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode"
)

// Position is a point in a series' reading order: everything in earlier
// books of the series, plus this book up to and including sentence Ord.
type Position struct {
	SeriesID    int64
	SeriesIndex float64
	BookID      int64
	Ord         int
}

// At returns the position just after sentence ord of b.
func At(b Book, ord int) Position {
	return Position{SeriesID: b.SeriesID, SeriesIndex: b.SeriesIndex, BookID: b.ID, Ord: ord}
}

// visible builds the WHERE fragment that hides anything revealed after pos.
// bookAlias must be a joined books table and ordCol the revealing ord.
func visible(pos Position, bookAlias, ordCol string) (string, []any) {
	q := bookAlias + ".series_id = ? AND (" + bookAlias + ".series_index < ? OR (" + bookAlias + ".id = ? AND " + ordCol + " <= ?))"
	return q, []any{pos.SeriesID, pos.SeriesIndex, pos.BookID, pos.Ord}
}

// NarratorName is the placeholder for a first-person narrator whose name
// the reader hasn't learned yet.
const NarratorName = "Narrator"

type EntitySummary struct {
	ID          int64
	Name        string
	Type        string
	Narrator    bool
	Aliases     []string
	FirstBookID int64
	FirstOrd    int
	Facts       int
	lastSeen    float64
	titleAlias  int64
	aliasIDs    []int64 // parallel to Aliases
}

// VisibleEntities lists the entities a reader at pos has met, with only the
// aliases revealed by then, in order of first appearance.
func (s *Store) VisibleEntities(ctx context.Context, pos Position) ([]EntitySummary, error) {
	where, args := visible(pos, "b", "e.first_ord")
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.name, e.type, e.narrator, e.first_book_id, e.first_ord, COALESCE(e.title_alias_id, 0)
		FROM entities e JOIN books b ON b.id = e.first_book_id
		WHERE `+where+` ORDER BY b.series_index, e.first_ord, e.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []EntitySummary
	idx := map[int64]int{}
	for rows.Next() {
		var e EntitySummary
		if err := rows.Scan(&e.ID, &e.Name, &e.Type, &e.Narrator, &e.FirstBookID, &e.FirstOrd, &e.titleAlias); err != nil {
			rows.Close()
			return nil, err
		}
		idx[e.ID] = len(out)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	where, args = visible(pos, "b", "a.revealed_ord")
	rows, err = s.db.QueryContext(ctx, `SELECT a.id, a.entity_id, a.alias FROM aliases a JOIN books b ON b.id = a.book_id
		WHERE `+where+` ORDER BY b.series_index, a.revealed_ord, a.id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var aid, id int64
		var alias string
		if err := rows.Scan(&aid, &id, &alias); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Aliases = append(out[i].Aliases, alias)
			out[i].aliasIDs = append(out[i].aliasIDs, aid)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		retitle(&out[i])
	}

	where, args = visible(pos, "b", "f.revealed_ord")
	rows, err = s.db.QueryContext(ctx, `SELECT f.entity_id, COUNT(*), MAX(b.series_index * 10000000 + f.revealed_ord)
		FROM facts f JOIN books b ON b.id = f.book_id WHERE `+where+` GROUP BY f.entity_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		var last float64
		if err := rows.Scan(&id, &n, &last); err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Facts, out[i].lastSeen = n, last
		}
	}
	return out, rows.Err()
}

// retitle shows an entry by the name the reader knows it best by, among the
// names already revealed: the title chosen for it once that alias is
// revealed, or else, for an entry first noted by a description ("the
// coachman", or a narrator's placeholder), the first proper name revealed.
func retitle(e *EntitySummary) {
	pick := -1
	for j, id := range e.aliasIDs {
		if id == e.titleAlias {
			pick = j
		}
	}
	placeholder := e.Narrator && e.Name == NarratorName
	if pick < 0 && (placeholder || startsLower(e.Name)) {
		for j, a := range e.Aliases {
			if properName(a) {
				pick = j
				break
			}
		}
	}
	if pick < 0 {
		return
	}
	rest := make([]string, 0, len(e.Aliases))
	if !placeholder {
		rest = append(rest, e.Name)
	}
	rest = append(rest, e.Aliases[:pick]...)
	rest = append(rest, e.Aliases[pick+1:]...)
	e.Name, e.Aliases = e.Aliases[pick], rest
}

func startsLower(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}

// properName reports whether a name looks like a name rather than a
// description: capitalized, and not "The ..." or "A ...".
func properName(s string) bool {
	for _, r := range s {
		if !unicode.IsUpper(r) {
			return false
		}
		break
	}
	first, _, _ := strings.Cut(s, " ")
	switch first {
	case "The", "A", "An":
		return false
	}
	return s != ""
}

// MostRecent returns up to n entities with the latest visible facts.
func MostRecent(es []EntitySummary, n int) []EntitySummary {
	sorted := append([]EntitySummary(nil), es...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].lastSeen > sorted[j-1].lastSeen; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

type Fact struct {
	Text        string
	BookID      int64
	BookTitle   string
	SeriesIndex float64
	Ord         int
	Evidence    []int
}

// VisibleFacts returns an entity's facts revealed by pos, oldest first. With
// limit > 0 it returns only the newest limit facts (still oldest first).
func (s *Store) VisibleFacts(ctx context.Context, entityID int64, pos Position, limit int) ([]Fact, error) {
	where, args := visible(pos, "b", "f.revealed_ord")
	q := `SELECT f.text, f.book_id, b.title, b.series_index, f.revealed_ord, f.evidence
		FROM facts f JOIN books b ON b.id = f.book_id
		WHERE f.entity_id = ? AND ` + where + ` ORDER BY b.series_index DESC, f.revealed_ord DESC, f.id DESC`
	args = append([]any{entityID}, args...)
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var ev string
		if err := rows.Scan(&f.Text, &f.BookID, &f.BookTitle, &f.SeriesIndex, &f.Ord, &ev); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ev), &f.Evidence); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

type Marker struct {
	Label     string
	BookID    int64
	BookTitle string
	Ord       int
}

// VisibleMarkers returns in-world time markers reached by pos.
func (s *Store) VisibleMarkers(ctx context.Context, pos Position) ([]Marker, error) {
	where, args := visible(pos, "b", "m.ord")
	rows, err := s.db.QueryContext(ctx, `SELECT m.label, m.book_id, b.title, m.ord FROM time_markers m JOIN books b ON b.id = m.book_id
		WHERE `+where+` ORDER BY b.series_index, m.ord`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Marker
	for rows.Next() {
		var m Marker
		if err := rows.Scan(&m.Label, &m.BookID, &m.BookTitle, &m.Ord); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type AliasRec struct {
	Alias string
	Ord   int
}

type FactRec struct {
	Text     string
	Ord      int
	Evidence []int
}

type NewEntity struct {
	Name     string
	Type     string
	Narrator bool
	FirstOrd int
	Aliases  []AliasRec
	Facts    []FactRec
}

type EntityUpdate struct {
	ID          int64
	SetNarrator bool
	Aliases     []AliasRec
	Facts       []FactRec
}

type MarkerRec struct {
	Label string
	Ord   int
}

type UsageRec struct {
	UserID       int64
	KeyOwnerID   int64
	Provider     string
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// ChunkWrite is everything validated from one extraction chunk. It is
// applied atomically together with the progress marker, so a crash never
// leaves a chunk half-written or recorded twice.
type ChunkWrite struct {
	BookID    int64
	SeriesID  int64
	NextChunk int
	New       []NewEntity
	Updates   []EntityUpdate
	Markers   []MarkerRec
	Usage     *UsageRec
}

type ChunkStats struct {
	NewEntities int
	Facts       int
	Aliases     int
	Duplicates  int
}

func (s *Store) ApplyChunk(ctx context.Context, w ChunkWrite) (ChunkStats, error) {
	var st ChunkStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	for _, e := range w.New {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO entities (series_id, name, type, narrator, first_book_id, first_ord) VALUES (?, ?, ?, ?, ?, ?)",
			w.SeriesID, e.Name, e.Type, e.Narrator, w.BookID, e.FirstOrd)
		if err != nil {
			return st, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return st, err
		}
		st.NewEntities++
		if err := addDetails(ctx, tx, &st, w.BookID, id, e.Aliases, e.Facts); err != nil {
			return st, err
		}
	}
	for _, u := range w.Updates {
		if u.SetNarrator {
			if _, err := tx.ExecContext(ctx, "UPDATE entities SET narrator = 1 WHERE id = ?", u.ID); err != nil {
				return st, err
			}
		}
		if err := addDetails(ctx, tx, &st, w.BookID, u.ID, u.Aliases, u.Facts); err != nil {
			return st, err
		}
	}
	for _, m := range w.Markers {
		if _, err := tx.ExecContext(ctx, "INSERT INTO time_markers (book_id, label, ord) VALUES (?, ?, ?)", w.BookID, m.Label, m.Ord); err != nil {
			return st, err
		}
	}
	if u := w.Usage; u != nil {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO usage (user_id, key_owner_id, book_id, provider, model, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)",
			u.UserID, u.KeyOwnerID, w.BookID, u.Provider, u.Model, u.InputTokens, u.OutputTokens); err != nil {
			return st, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ingest_progress SET next_chunk = ? WHERE book_id = ?", w.NextChunk, w.BookID); err != nil {
		return st, err
	}
	return st, tx.Commit()
}

func addDetails(ctx context.Context, tx *sql.Tx, st *ChunkStats, bookID, entityID int64, aliases []AliasRec, facts []FactRec) error {
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM entities WHERE id = ?", entityID).Scan(&name); err != nil {
		return err
	}
	for _, a := range aliases {
		if strings.EqualFold(a.Alias, name) {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM aliases WHERE entity_id = ? AND lower(alias) = lower(?)", entityID, a.Alias).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO aliases (entity_id, alias, book_id, revealed_ord) VALUES (?, ?, ?, ?)",
			entityID, a.Alias, bookID, a.Ord); err != nil {
			return err
		}
		st.Aliases++
	}
	for _, f := range facts {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM facts WHERE entity_id = ? AND lower(text) = lower(?)", entityID, f.Text).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			st.Duplicates++
			continue
		}
		ev, err := json.Marshal(f.Evidence)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO facts (entity_id, text, book_id, revealed_ord, evidence) VALUES (?, ?, ?, ?, ?)",
			entityID, f.Text, bookID, f.Ord, string(ev)); err != nil {
			return err
		}
		st.Facts++
	}
	return nil
}
