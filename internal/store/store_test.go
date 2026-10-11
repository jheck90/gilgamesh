package store

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addBook(t *testing.T, s *Store, seriesID int64, idx float64, title string, n int) Book {
	t.Helper()
	ctx := context.Background()
	b := Book{SeriesID: seriesID, SeriesIndex: idx, Title: title, SourcePath: title + ".epub", SHA256: title}
	var sents []Sentence
	for i := 0; i < n; i++ {
		sents = append(sents, Sentence{Ord: i, ChapterIdx: 1, Para: i, Text: "s"})
	}
	id, err := s.CreateBook(ctx, b, []Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: n - 1}}, sents, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err = s.BookByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFirstUserIsAdmin(t *testing.T) {
	s, ctx := open(t), context.Background()
	a, err := s.EnsureUser(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.EnsureUser(ctx, "default")
	if !a.IsAdmin || b.IsAdmin || again.ID != a.ID || !again.IsAdmin {
		t.Errorf("admin flags wrong: %+v %+v %+v", a, b, again)
	}
}

func TestVisibility(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b1 := addBook(t, s, sid, 1, "Book One", 100)
	b2 := addBook(t, s, sid, 2, "Book Two", 100)

	// Book one: Strider appears at 10, is revealed as Aragorn at 80.
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b1.ID, SeriesID: sid, NextChunk: 1,
		New: []NewEntity{{Name: "Strider", Type: "character", FirstOrd: 10,
			Aliases: []AliasRec{{Alias: "Aragorn", Ord: 80}},
			Facts: []FactRec{
				{Text: "Strider waited at the inn.", Ord: 10, Evidence: []int{10}},
				{Text: "Strider is heir to the throne.", Ord: 80, Evidence: []int{79, 80}},
			}}},
		Usage: &UsageRec{UserID: 1, KeyOwnerID: 1, Provider: "x", Model: "m", InputTokens: 5, OutputTokens: 6},
	}); err == nil {
		t.Fatal("usage for a missing user should violate the foreign key")
	}
	u, _ := s.EnsureUser(ctx, "default")
	st, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b1.ID, SeriesID: sid, NextChunk: 1,
		New: []NewEntity{{Name: "Strider", Type: "character", FirstOrd: 10,
			Aliases: []AliasRec{{Alias: "Aragorn", Ord: 80}, {Alias: "strider", Ord: 10}},
			Facts: []FactRec{
				{Text: "Strider waited at the inn.", Ord: 10, Evidence: []int{10}},
				{Text: "Strider is heir to the throne.", Ord: 80, Evidence: []int{79, 80}},
				{Text: "strider waited at the inn.", Ord: 12, Evidence: []int{12}},
			}}},
		Usage: &UsageRec{UserID: u.ID, KeyOwnerID: u.ID, Provider: "x", Model: "m", InputTokens: 5, OutputTokens: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.NewEntities != 1 || st.Facts != 2 || st.Aliases != 1 || st.Duplicates != 1 {
		t.Errorf("stats = %+v", st)
	}

	check := func(pos Position, wantEntities, wantFacts int, wantAliases []string) {
		t.Helper()
		es, err := s.VisibleEntities(ctx, pos)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) != wantEntities {
			t.Fatalf("at %+v: %d entities, want %d", pos, len(es), wantEntities)
		}
		if wantEntities == 0 {
			return
		}
		fs, err := s.VisibleFacts(ctx, es[0].ID, pos, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(fs) != wantFacts || es[0].Facts != wantFacts {
			t.Errorf("at %+v: %d facts (summary says %d), want %d", pos, len(fs), es[0].Facts, wantFacts)
		}
		if len(es[0].Aliases) != len(wantAliases) {
			t.Errorf("at %+v: aliases %q, want %q", pos, es[0].Aliases, wantAliases)
		}
	}
	check(At(b1, 9), 0, 0, nil)
	check(At(b1, 10), 1, 1, nil)
	check(At(b1, 79), 1, 1, nil)
	check(At(b1, 80), 1, 2, []string{"Aragorn"})
	check(At(b2, 0), 1, 2, []string{"Aragorn"}) // earlier books count as read

	// Reading book one again later must not see book two's facts.
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b2.ID, SeriesID: sid, NextChunk: 1,
		Updates: []EntityUpdate{{ID: 1, Facts: []FactRec{{Text: "Aragorn was crowned.", Ord: 50, Evidence: []int{50}}}}},
	}); err != nil {
		t.Fatal(err)
	}
	check(At(b1, 99), 1, 2, []string{"Aragorn"})
	check(At(b2, 50), 1, 3, []string{"Aragorn"})

	recent, err := s.VisibleFacts(ctx, 1, At(b2, 99), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[1].Text != "Aragorn was crowned." {
		t.Errorf("newest two facts = %+v", recent)
	}

	p, _ := s.Progress(ctx, b1.ID)
	if !p.Done() {
		t.Errorf("progress = %+v, want done", p)
	}
}

func TestGrants(t *testing.T) {
	s, ctx := open(t), context.Background()
	admin, _ := s.EnsureUser(ctx, "default")
	alice, _ := s.EnsureUser(ctx, "alice")
	if err := s.DecideGrant(ctx, alice.ID, GrantApproved, 0, admin.ID); err == nil {
		t.Error("approving without a request should fail")
	}
	if err := s.RequestGrant(ctx, alice.ID, "please"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideGrant(ctx, alice.ID, GrantApproved, 1000, admin.ID); err != nil {
		t.Fatal(err)
	}
	// Asking again must not knock an approved grant back to requested.
	if err := s.RequestGrant(ctx, alice.ID, "again"); err != nil {
		t.Fatal(err)
	}
	g, ok, err := s.GetGrant(ctx, alice.ID)
	if err != nil || !ok || g.Status != GrantApproved || g.MonthlyCap != 1000 {
		t.Errorf("grant = %+v ok=%v err=%v", g, ok, err)
	}
}

func TestNarratorTakesRevealedName(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	b := addBook(t, s, sid, 1, "One", 50)
	if _, err := s.ApplyChunk(ctx, ChunkWrite{BookID: b.ID, SeriesID: sid, NextChunk: 1,
		New: []NewEntity{{Name: NarratorName, Type: "character", Narrator: true, FirstOrd: 0,
			Aliases: []AliasRec{{Alias: "Jim", Ord: 30}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ord  int
		want string
	}{{29, NarratorName}, {30, "Jim"}} {
		es, err := s.VisibleEntities(ctx, At(b, c.ord))
		if err != nil || len(es) != 1 || es[0].Name != c.want || len(es[0].Aliases) != 0 {
			t.Errorf("at %d: %+v, %v; want name %q and no aliases", c.ord, es, err, c.want)
		}
	}
}
