package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUsageHistory(t *testing.T) {
	s, ctx := open(t), context.Background()
	sid, _ := s.EnsureSeries(ctx, "Saga")
	u, _ := s.EnsureUser(ctx, "default")
	one := addBook(t, s, sid, 1, "One", 10) // ten one-word sentences
	two := addBook(t, s, sid, 2, "Two", 4)
	rec := func(model string, in, out int64) *UsageRec {
		return &UsageRec{UserID: u.ID, KeyOwnerID: u.ID, Provider: "anthropic", Model: model, Effort: "medium", InputTokens: in, OutputTokens: out}
	}
	for _, w := range []ChunkWrite{
		{BookID: one.ID, SeriesID: sid, NextChunk: 1, Usage: rec("haiku", 1000, 100)},
		{BookID: one.ID, SeriesID: sid, NextChunk: 1, Usage: rec("haiku", 3000, 500)},
		{BookID: two.ID, SeriesID: sid, NextChunk: 1, Usage: rec("haiku", 2000, 400)},
		{BookID: two.ID, SeriesID: sid, NextChunk: 1, Usage: rec("opus", 9000, 900)},
		// Read again at another effort: a run of its own.
		{BookID: two.ID, SeriesID: sid, NextChunk: 1, Usage: &UsageRec{UserID: u.ID, KeyOwnerID: u.ID, Provider: "anthropic", Model: "haiku", Effort: "high", InputTokens: 100, OutputTokens: 50}},
	} {
		if _, err := s.ApplyChunk(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplyEdit(ctx, Edit{BookID: one.ID, SeriesID: sid, Model: "opus", Usage: rec("opus", 600, 60)}); err != nil {
		t.Fatal(err)
	}

	got, err := s.IngestUsage(ctx, "haiku")
	if err != nil {
		t.Fatal(err)
	}
	want := []BookUsage{{Effort: "medium", Input: 4000, Output: 600, Words: 10}, {Effort: "high", Input: 100, Output: 50, Words: 4}, {Effort: "medium", Input: 2000, Output: 400, Words: 4}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("IngestUsage(haiku) = %+v, want %+v", got, want)
	}
	// The edit's tokens aren't counted as reading the book.
	if got, err := s.IngestUsage(ctx, "opus"); err != nil || len(got) != 1 || got[0].Input != 9000 {
		t.Errorf("IngestUsage(opus) = %+v, %v; want only book two's 9000", got, err)
	}
	got, err = s.EditUsage(ctx, "opus")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (BookUsage{Effort: "medium", Input: 600, Output: 60, Words: 10}) {
		t.Errorf("EditUsage(opus) = %+v, want one edit of book one", got)
	}
}

// Usage recorded before kinds existed: a finished book's first rows read
// it, and anything after them (an edit, or its dry run) tidied its notes.
func TestMigrateUsageKinds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := s.EnsureSeries(ctx, "Saga")
	u, _ := s.EnsureUser(ctx, "default")
	done := addBook(t, s, sid, 1, "One", 10) // one chunk
	reading := addBook(t, s, sid, 2, "Two", 10)
	if _, err := s.db.Exec(`ALTER TABLE usage DROP COLUMN kind; ALTER TABLE usage DROP COLUMN effort; PRAGMA user_version = 3;
		UPDATE ingest_progress SET next_chunk = 1 WHERE book_id = ?1;
		UPDATE ingest_progress SET total_chunks = 3, next_chunk = 1 WHERE book_id = ?2;`, done.ID, reading.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO usage (user_id, key_owner_id, book_id, provider, model, input_tokens, output_tokens) VALUES
		  (?1, ?1, ?2, 'anthropic', 'opus', 1000, 100),
		  (?1, ?1, ?2, 'anthropic', 'opus', 600, 60),
		  (?1, ?1, ?3, 'anthropic', 'opus', 900, 90),
		  (?1, ?1, ?3, 'anthropic', 'opus', 800, 80);`, u.ID, done.ID, reading.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.EditUsage(ctx, "opus"); err != nil || len(got) != 1 || got[0].Input != 600 {
		t.Errorf("EditUsage after migrating = %+v, %v; want the finished book's second call", got, err)
	}
	got, err := s.IngestUsage(ctx, "opus")
	if err != nil || len(got) != 2 || got[0].Input != 1000 || got[1].Input != 1700 {
		t.Errorf("IngestUsage after migrating = %+v, %v; want 1000 for the finished book and 1700 for the one being read", got, err)
	}
}
