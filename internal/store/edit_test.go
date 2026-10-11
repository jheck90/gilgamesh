package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func ids(t *testing.T, s *Store, b Book) map[string]NoteEntity {
	t.Helper()
	n, err := s.BookNotes(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]NoteEntity{}
	for _, e := range n.Entities {
		out[e.Name] = e
	}
	return out
}

func TestApplyEditMerge(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b1 := addBook(t, s, sid, 1, "One", 100)
	b2 := addBook(t, s, sid, 2, "Two", 100)
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b1.ID, SeriesID: sid, NextChunk: 1, New: []NewEntity{
		{Name: "Vrie Guyen", Type: "character", FirstOrd: 10,
			Aliases: []AliasRec{{Alias: "the commander", Ord: 12}},
			Facts:   []FactRec{{Text: "Vrie Guyen commands the ark.", Ord: 10, Evidence: []int{10}}}},
		{Name: "Guyen", Type: "character", FirstOrd: 30,
			Aliases: []AliasRec{{Alias: "The Commander", Ord: 31}, {Alias: "the shepherd", Ord: 40}},
			Facts: []FactRec{
				{Text: "Guyen gave the order.", Ord: 30, Evidence: []int{30}},
				{Text: "vrie guyen commands the ark.", Ord: 35, Evidence: []int{35}},
			}},
		{Name: "Isa Lain", Type: "character", FirstOrd: 50, Facts: []FactRec{
			{Text: "Isa Lain is an engineer.", Ord: 45, Evidence: []int{45}},
			{Text: "Isa Lain mended the hull.", Ord: 55, Evidence: []int{55}},
		}},
		{Name: "Lain", Type: "character", FirstOrd: 60, Facts: []FactRec{{Text: "Lain fixed it.", Ord: 60, Evidence: []int{60}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b2.ID, SeriesID: sid, NextChunk: 1, New: []NewEntity{
		{Name: "Guyen the Younger", Type: "character", FirstOrd: 5, Facts: []FactRec{{Text: "He is new.", Ord: 5, Evidence: []int{5}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	e1 := ids(t, s, b1)
	vrie, guyen, isa, lain := e1["Vrie Guyen"], e1["Guyen"], e1["Isa Lain"], e1["Lain"]
	younger := ids(t, s, b2)["Guyen the Younger"]

	// Book two's entity can't be folded away by an edit of book one, and
	// the good merge before it must be rolled back too.
	if _, err := s.ApplyEdit(ctx, Edit{BookID: b1.ID, SeriesID: sid, Merges: []Merge{
		{Into: vrie.ID, From: []int64{guyen.ID}}, {Into: vrie.ID, From: []int64{younger.ID}}}}); err == nil {
		t.Error("folding away another book's entity should fail")
	}
	if n := ids(t, s, b1); len(n) != 4 || len(n["Vrie Guyen"].Facts) != 1 {
		t.Fatalf("a failed edit changed things: %+v", n)
	}

	// Folding Isa Lain into Lain, met later: her notes wait for Lain's
	// entry, and her fact from before her own name appeared waits for it.
	if _, err := s.ApplyEdit(ctx, Edit{BookID: b1.ID, SeriesID: sid, Merges: []Merge{{Into: lain.ID, From: []int64{isa.ID}}}}); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.VisibleEntities(ctx, At(b1, 59)); len(es) != 2 {
		t.Errorf("at 59: %d entities, want only the two Guyens (Isa Lain's notes now wait for Lain)", len(es))
	}
	for ord, want := range map[int]int{49: 0, 50: 1, 60: 3} {
		if fs, _ := s.VisibleFacts(ctx, lain.ID, At(b1, ord), 0); len(fs) != want {
			t.Errorf("at %d Lain has %d facts, want %d", ord, len(fs), want)
		}
	}

	st, err := s.ApplyEdit(ctx, Edit{BookID: b1.ID, SeriesID: sid, Merges: []Merge{{Into: vrie.ID, From: []int64{guyen.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Merged != 1 {
		t.Errorf("stats = %+v", st)
	}
	after := ids(t, s, b1)
	if _, ok := after["Guyen"]; ok {
		t.Error("Guyen should be gone")
	}
	if a := after["Lain"].Aliases; len(a) != 1 || a[0].Alias != "Isa Lain" || a[0].Ord != 50 {
		t.Errorf("Lain's aliases = %+v, want Isa Lain at 50", a)
	}
	if got := len(after["Vrie Guyen"].Facts); got != 2 {
		t.Errorf("Vrie Guyen has %d facts, want 2 (the repeated one dropped)", got)
	}

	aliasesAt := func(ord int) []string {
		es, err := s.VisibleEntities(ctx, At(b1, ord))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.ID == vrie.ID {
				sort.Strings(e.Aliases)
				return e.Aliases
			}
		}
		t.Fatalf("Vrie Guyen not visible at %d", ord)
		return nil
	}
	if got := aliasesAt(29); len(got) != 1 || got[0] != "the commander" {
		t.Errorf("aliases at 29 = %q, want only the commander", got)
	}
	if got := aliasesAt(40); len(got) != 3 || got[0] != "Guyen" || got[2] != "the shepherd" {
		t.Errorf("aliases at 40 = %q, want Guyen, the commander, the shepherd", got)
	}
	if fs, _ := s.VisibleFacts(ctx, vrie.ID, At(b1, 29), 0); len(fs) != 1 {
		t.Errorf("at 29 Vrie Guyen shows %d facts, want 1 (Guyen's are revealed at 30)", len(fs))
	}
}

func TestApplyEditDropsStayInBook(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b1 := addBook(t, s, sid, 1, "One", 100)
	b2 := addBook(t, s, sid, 2, "Two", 100)
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b1.ID, SeriesID: sid, NextChunk: 1,
		New: []NewEntity{
			{Name: "Carl", Type: "character", FirstOrd: 1, Facts: []FactRec{{Text: "Carl woke.", Ord: 1, Evidence: []int{1}}}},
			{Name: "Narrator", Type: "character", Narrator: true, FirstOrd: 0, Facts: []FactRec{{Text: "The narrator is tired.", Ord: 2, Evidence: []int{2}}}},
			{Name: "Mongo", Type: "creature", FirstOrd: 3, Facts: []FactRec{{Text: "Mongo is a dinosaur.", Ord: 3, Evidence: []int{3}}}},
		},
		Markers: []MarkerRec{{Label: "That night", Ord: 4}}}); err != nil {
		t.Fatal(err)
	}
	e := ids(t, s, b1)
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b2.ID, SeriesID: sid, NextChunk: 1,
		Updates: []EntityUpdate{{ID: e["Carl"].ID, Facts: []FactRec{{Text: "Carl climbed.", Ord: 1, Evidence: []int{1}}}}}}); err != nil {
		t.Fatal(err)
	}
	n2, _ := s.BookNotes(ctx, b2)
	if len(n2.Entities) != 1 || len(n2.Entities[0].Facts) != 1 || n2.Entities[0].OtherFacts != 1 {
		t.Fatalf("book two notes = %+v", n2.Entities)
	}
	bookTwoFact := n2.Entities[0].Facts[0].ID
	n1, _ := s.BookNotes(ctx, b1)

	st, err := s.ApplyEdit(ctx, Edit{BookID: b1.ID, SeriesID: sid,
		CategoryMap: map[string]string{"creature": "character"},
		DropEntity:  []int64{e["Narrator"].ID},
		DropFact:    []int64{bookTwoFact, e["Carl"].Facts[0].ID},
		DropMarker:  []int64{n1.Markers[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := EditStats{Retyped: 1, Facts: 1, Markers: 1}
	if st != want {
		t.Errorf("stats = %+v, want %+v (the narrator and book two's fact are out of reach)", st, want)
	}
	if got := ids(t, s, b1)["Mongo"].Type; got != "character" {
		t.Errorf("Mongo's type = %q", got)
	}

	backup := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	c, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := ids(t, c, b1); len(got) != 3 {
		t.Errorf("backup has %d entities, want 3", len(got))
	}
}

func TestEditsAreRecorded(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b := addBook(t, s, sid, 1, "One", 10)
	u, _ := s.EnsureUser(ctx, "default")
	usageOnly := Edit{BookID: b.ID, SeriesID: sid, Usage: &UsageRec{UserID: u.ID, KeyOwnerID: u.ID, Provider: "x", Model: "m"}}
	if _, err := s.ApplyEdit(ctx, usageOnly); err != nil {
		t.Fatal(err)
	}
	if _, edited, err := s.LastEdit(ctx, b.ID); err != nil || edited {
		t.Errorf("recording usage counted as an edit: %v %v", edited, err)
	}
	if _, err := s.ApplyEdit(ctx, Edit{BookID: b.ID, SeriesID: sid, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if at, edited, err := s.LastEdit(ctx, b.ID); err != nil || !edited || at == "" {
		t.Errorf("LastEdit = %q %v %v, want a time", at, edited, err)
	}
}

func TestMigrateFromV1(t *testing.T) {
	// A database as the first release created it, with a book in it.
	v1, err := os.ReadFile("testdata/schema_v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(v1) + `; PRAGMA user_version = 1;
		INSERT INTO series (id, name) VALUES (1, 'Saga');
		INSERT INTO books (id, series_id, title, source_path, sha256) VALUES (1, 1, 'One', 'one.epub', 'x');
		INSERT INTO entities (id, series_id, name, type, first_book_id, first_ord) VALUES (1, 1, 'the coachman', 'character', 1, 5);
		INSERT INTO aliases (entity_id, alias, book_id, revealed_ord) VALUES (1, 'Williams', 1, 9);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	s.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != schemaVersion {
		t.Errorf("version = %d, want %d", v, schemaVersion)
	}
	ctx := context.Background()
	if _, _, err := s.LastEdit(ctx, 1); err != nil {
		t.Errorf("edits table missing after migrating: %v", err)
	}
	b, _, err := s.BookByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	es, err := s.VisibleEntities(ctx, At(b, 9))
	if err != nil || len(es) != 1 || es[0].Name != "Williams" {
		t.Errorf("after migrating: %+v, %v; want the coachman shown as Williams", es, err)
	}
}

func TestTitles(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b := addBook(t, s, sid, 1, "One", 100)
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b.ID, SeriesID: sid, NextChunk: 1, New: []NewEntity{
		{Name: "the murderer", Type: "character", FirstOrd: 10, Aliases: []AliasRec{
			{Alias: "Square-toes", Ord: 20}, {Alias: "the cabman", Ord: 30}, {Alias: "Jefferson Hope", Ord: 40}}},
		{Name: "Henry Jekyll, M.D., F.R.S.", Type: "character", FirstOrd: 12, Aliases: []AliasRec{{Alias: "Dr. Jekyll", Ord: 12}}},
	}}); err != nil {
		t.Fatal(err)
	}
	n := ids(t, s, b)
	murderer, jekyll := n["the murderer"], n["Henry Jekyll, M.D., F.R.S."]
	name := func(ord int, id int64) (string, []string) {
		t.Helper()
		es, err := s.VisibleEntities(ctx, At(b, ord))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.ID == id {
				return e.Name, e.Aliases
			}
		}
		return "", nil
	}

	// A description gives way to the first proper name the reader learns.
	for ord, want := range map[int]string{15: "the murderer", 25: "Square-toes", 45: "Square-toes"} {
		if got, _ := name(ord, murderer.ID); got != want {
			t.Errorf("at %d the murderer is titled %q, want %q", ord, got, want)
		}
	}

	hope, drJekyll := murderer.Aliases[2].ID, jekyll.Aliases[0].ID
	st, err := s.ApplyEdit(ctx, Edit{BookID: b.ID, SeriesID: sid, Titles: []Title{
		{Entity: murderer.ID, Alias: hope}, {Entity: jekyll.ID, Alias: drJekyll}, {Entity: jekyll.ID, Alias: hope}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Titled != 2 {
		t.Errorf("titled %d, want 2 (an alias of another entry can't be a title)", st.Titled)
	}
	// The chosen title waits for its own reveal.
	for ord, want := range map[int]string{25: "Square-toes", 39: "Square-toes", 40: "Jefferson Hope"} {
		if got, _ := name(ord, murderer.ID); got != want {
			t.Errorf("at %d the murderer is titled %q, want %q", ord, got, want)
		}
	}
	if got, aliases := name(40, murderer.ID); got != "Jefferson Hope" || len(aliases) != 3 || aliases[0] != "the murderer" {
		t.Errorf("at 40: %q also %q", got, aliases)
	}
	if got, aliases := name(12, jekyll.ID); got != "Dr. Jekyll" || len(aliases) != 1 {
		t.Errorf("Jekyll at 12: %q also %q", got, aliases)
	}
}
