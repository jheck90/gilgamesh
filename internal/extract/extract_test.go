package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

func testChunk() chunk {
	return chunk{
		Chapter: store.Chapter{Idx: 2, Title: "Two"},
		Part:    1, Parts: 1,
		Sentences: []store.Sentence{
			{Ord: 10, Para: 5, Text: "The hooded man watched Carl."},
			{Ord: 11, Para: 5, Text: "Donut ignored him."},
			{Ord: 12, Para: 6, Text: "Everyone called the hooded man Mordecai."},
		},
	}
}

func parse(t *testing.T, s string) output {
	t.Helper()
	var o output
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestValidate(t *testing.T) {
	k := known{
		cands: map[int64]candidate{
			1: {EntitySummary: store.EntitySummary{ID: 1, Name: "Carl", Type: "character"}},
			2: {EntitySummary: store.EntitySummary{ID: 2, Name: "Princess Donut", Aliases: []string{"Donut"}}},
			3: {EntitySummary: store.EntitySummary{ID: 3, Name: "Odette", Type: "character"}},
			4: {EntitySummary: store.EntitySummary{ID: 4, Name: "Narrator", Narrator: true}},
		},
		byName: map[string]int64{"carl": 1, "princess donut": 2, "donut": 2, "odette": 3, "narrator": 4},
	}
	out := parse(t, `{"entities": [
		{"entity_id": "E1", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Carl was  watched.", "sentences": [1]}, {"text": "Carl will die.", "sentences": [9]}, {"text": "No cite.", "sentences": []}]},
		{"entity_id": "E3", "name": "Odette", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Odette is secretly the hooded man.", "sentences": [1]}]},
		{"entity_id": "E4", "name": "Narrator", "type": "character", "is_narrator": true, "new_aliases": [],
		 "facts": [{"text": "The narrator was tired.", "sentences": [2]}]},
		{"entity_id": "E99", "name": "Ghost", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Ghost.", "sentences": [1]}]},
		{"entity_id": "", "name": "Mordecai", "type": "Character ", "is_narrator": false,
		 "new_aliases": [{"alias": "the hooded man", "sentence": 1}, {"alias": "Mordy", "sentence": 3}],
		 "facts": [{"text": "Mordecai watched Carl.", "sentences": [3, 1]}]},
		{"entity_id": "", "name": "Aragorn", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Aragorn is king.", "sentences": [1]}]},
		{"entity_id": "", "name": "Donut", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Donut ignored the hooded man.", "sentences": [2]}]}
	], "time_markers": [{"label": "Day 2", "sentence": 2}, {"label": "Day 9", "sentence": 7}]}`)

	w, warns := validate(out, testChunk(), k)

	if len(w.New) != 1 {
		t.Fatalf("new entities = %+v", w.New)
	}
	m := w.New[0]
	if m.Name != "Mordecai" || m.Type != "character" || m.FirstOrd != 12 {
		t.Errorf("Mordecai = %+v (first appearance should be where the name is, ord 12)", m)
	}
	if len(m.Aliases) != 1 || m.Aliases[0].Alias != "the hooded man" || m.Aliases[0].Ord != 10 {
		t.Errorf("Mordecai aliases = %+v (Mordy isn't in sentence 3)", m.Aliases)
	}
	if len(m.Facts) != 1 || m.Facts[0].Ord != 12 || len(m.Facts[0].Evidence) != 2 || m.Facts[0].Evidence[0] != 10 {
		t.Errorf("Mordecai facts = %+v (a fact is revealed at its last cited sentence)", m.Facts)
	}

	byID := map[int64]store.EntityUpdate{}
	for _, u := range w.Updates {
		byID[u.ID] = u
	}
	if u := byID[1]; len(u.Facts) != 1 || u.Facts[0].Text != "Carl was watched." {
		t.Errorf("Carl update = %+v", u)
	}
	if _, ok := byID[3]; ok {
		t.Error("Odette got a fact in a passage that never names her")
	}
	if u := byID[4]; len(u.Facts) != 1 {
		t.Errorf("narrator should be exempt from the name check: %+v", u)
	}
	if u := byID[2]; len(u.Facts) != 1 {
		t.Errorf("a new entity named like a known alias should merge into it: %+v", u)
	}
	if len(w.Markers) != 1 || w.Markers[0].Ord != 11 {
		t.Errorf("markers = %+v", w.Markers)
	}
	// Carl x2, Odette, Ghost, Mordy, Aragorn, Day 9.
	if len(warns) != 7 {
		t.Errorf("got %d warnings, want 7:\n%s", len(warns), strings.Join(warns, "\n"))
	}
}

func TestBuildChunks(t *testing.T) {
	chapters := []store.Chapter{{Idx: 1, FirstOrd: 0, LastOrd: 3}, {Idx: 2, FirstOrd: 4, LastOrd: 4}}
	sentences := []store.Sentence{
		{Ord: 0, Para: 0, Text: "one two three"},
		{Ord: 1, Para: 0, Text: "four five"},
		{Ord: 2, Para: 1, Text: "six seven eight"},
		{Ord: 3, Para: 2, Text: "nine"},
		{Ord: 4, Para: 3, Text: "ten"},
	}
	cs := buildChunks(chapters, sentences, 6)
	if len(cs) != 3 {
		t.Fatalf("got %d chunks: %+v", len(cs), cs)
	}
	if len(cs[0].Sentences) != 2 || cs[0].Parts != 2 || len(cs[1].Sentences) != 2 || cs[2].Chapter.Idx != 2 {
		t.Errorf("chunks split wrong: %+v", cs)
	}
}

func TestValidateMiscitedAlias(t *testing.T) {
	c := chunk{Chapter: store.Chapter{Idx: 1, Title: "One"}, Part: 1, Parts: 1, Sentences: []store.Sentence{
		{Ord: 10, Para: 1, Text: "A man in a hood waited by the stairs."},
		{Ord: 11, Para: 1, Text: "Everyone called him Mordecai."},
		{Ord: 12, Para: 2, Text: "The guide, as Carl thought of him, nodded."},
	}}
	out := parse(t, `{"entities": [{"entity_id": "", "name": "Mordecai", "type": "character", "is_narrator": false,
		"new_aliases": [{"alias": "the guide", "sentence": 1}, {"alias": "a man in a hood", "sentence": 2}, {"alias": "Mordy", "sentence": 2}],
		"facts": [{"text": "Mordecai waited by the stairs.", "sentences": [1, 2]}]}], "time_markers": []}`)

	w, warns := validate(out, c, known{cands: map[int64]candidate{}, byName: map[string]int64{}})
	if len(w.New) != 1 {
		t.Fatalf("new entities = %+v", w.New)
	}
	got := map[string]int{}
	for _, a := range w.New[0].Aliases {
		got[a.Alias] = a.Ord
	}
	// Cited before it appears: revealed where it appears. Cited after it
	// appears: revealed where the model said the link was made.
	if len(got) != 2 || got["the guide"] != 12 || got["a man in a hood"] != 11 {
		t.Errorf("aliases = %v, want the guide at 12 and a man in a hood at 11", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Mordy") {
		t.Errorf("warnings = %q, want one for Mordy, who isn't in the passage", warns)
	}
}

type fakeLLM struct {
	reply func(call int, req llm.Request) (string, error)
	calls int
}

func (f *fakeLLM) Provider() string { return "fake" }
func (f *fakeLLM) Model() string    { return "fake-1" }
func (f *fakeLLM) JSON(_ context.Context, req llm.Request) ([]byte, llm.Usage, error) {
	f.calls++
	s, err := f.reply(f.calls, req)
	return []byte(s), llm.Usage{InputTokens: 100, OutputTokens: 10}, err
}

func TestRunResumesAndHidesTheFuture(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, _ := st.EnsureUser(ctx, "default")
	sid, _ := st.EnsureSeries(ctx, "Saga")
	chapters := []store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 1}, {Idx: 2, Title: "Two", FirstOrd: 2, LastOrd: 4}}
	sentences := []store.Sentence{
		{Ord: 0, ChapterIdx: 1, Para: 0, Text: "Carl woke up in the dungeon."},
		{Ord: 1, ChapterIdx: 1, Para: 1, Text: "It was Day 1."},
		{Ord: 2, ChapterIdx: 2, Para: 2, Text: "The hooded man watched Carl."},
		{Ord: 3, ChapterIdx: 2, Para: 2, Text: "Carl waved."},
		{Ord: 4, ChapterIdx: 2, Para: 3, Text: "Everyone called the hooded man Mordecai."},
	}
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "Saga One", SHA256: "a", SourcePath: "a"},
		chapters, sentences, CountChunks(chapters, sentences, 1000), 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.BookByID(ctx, bid)

	chapterOne := `{"entities": [{"entity_id": "", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
		"facts": [{"text": "Carl woke up in a dungeon.", "sentences": [1]}]}], "time_markers": [{"label": "Day 1", "sentence": 2}]}`
	chapterTwo := `{"entities": [
		{"entity_id": "E1", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Carl waved at the hooded man.", "sentences": [1, 2]}]},
		{"entity_id": "", "name": "Mordecai", "type": "character", "is_narrator": false,
		 "new_aliases": [{"alias": "the hooded man", "sentence": 1}],
		 "facts": [{"text": "Mordecai watched Carl.", "sentences": [1, 3]}]}], "time_markers": []}`

	failing := &fakeLLM{reply: func(call int, req llm.Request) (string, error) {
		if call == 1 {
			if strings.Contains(req.User, "Mordecai") || strings.Contains(req.User, "hooded") {
				t.Error("chunk one's prompt contains text from chapter two")
			}
			return chapterOne, nil
		}
		return "", errors.New("network down")
	}}
	x := &Extractor{Store: st, LLM: failing, UserID: u.ID, KeyOwnerID: u.ID}
	if err := x.Run(ctx, b); err == nil {
		t.Fatal("expected the second chunk to fail")
	}
	if p, _ := st.Progress(ctx, b.ID); p.NextChunk != 1 {
		t.Fatalf("progress after failure = %+v, want next chunk 1", p)
	}
	if used, _ := st.TokensThisMonth(ctx, u.ID); used != 220 {
		t.Errorf("tokens recorded = %d, want 220 (the failed call counts too)", used)
	}

	ok := &fakeLLM{reply: func(call int, req llm.Request) (string, error) {
		if !strings.Contains(req.User, "E1 [character] Carl") || !strings.Contains(req.User, "Carl woke up in a dungeon.") {
			t.Errorf("chunk two should offer Carl with his facts:\n%s", req.User)
		}
		return chapterTwo, nil
	}}
	x.LLM = ok
	if err := x.Run(ctx, b); err != nil {
		t.Fatal(err)
	}
	if ok.calls != 1 {
		t.Errorf("resume re-ran %d chunks, want 1", ok.calls)
	}

	names := func(ord int) []string {
		es, err := st.VisibleEntities(ctx, store.At(b, ord))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}
	// Having read "The hooded man watched Carl", the reader doesn't know
	// his name yet.
	if got := names(3); len(got) != 1 || got[0] != "Carl" {
		t.Errorf("at ord 3 visible = %v, want only Carl", got)
	}
	if got := names(4); len(got) != 2 {
		t.Errorf("at ord 4 visible = %v, want Carl and Mordecai", got)
	}
	facts, _ := st.VisibleFacts(ctx, 1, store.At(b, 2), 0)
	if len(facts) != 1 {
		t.Errorf("at ord 2 Carl has %d facts, want 1 (the wave is revealed at ord 3)", len(facts))
	}
}

func TestRunRetriesAnEmptyReply(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, _ := st.EnsureUser(ctx, "default")
	sid, _ := st.EnsureSeries(ctx, "Saga")
	chapters := []store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 0}, {Idx: 2, Title: "Two", FirstOrd: 1, LastOrd: 1}}
	sentences := []store.Sentence{
		{Ord: 0, ChapterIdx: 1, Para: 0, Text: strings.Repeat("Carl walked on and on ", retryEmptyWords/5)},
		{Ord: 1, ChapterIdx: 2, Para: 1, Text: "Carl slept."},
	}
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "Saga One", SHA256: "a", SourcePath: "a"},
		chapters, sentences, CountChunks(chapters, sentences, 1000), 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.BookByID(ctx, bid)

	empty := `{"entities": [], "time_markers": []}`
	f := &fakeLLM{reply: func(call int, req llm.Request) (string, error) {
		if call == 2 {
			return `{"entities": [{"entity_id": "", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
				"facts": [{"text": "Carl walked a long way.", "sentences": [1]}]}], "time_markers": []}`, nil
		}
		return empty, nil
	}}
	x := &Extractor{Store: st, LLM: f, UserID: u.ID, KeyOwnerID: u.ID}
	if err := x.Run(ctx, b); err != nil {
		t.Fatal(err)
	}
	// The long chapter is asked twice; the one-sentence chapter is taken
	// at its word.
	if f.calls != 3 {
		t.Errorf("made %d calls, want 3", f.calls)
	}
	if facts, _ := st.VisibleFacts(ctx, 1, store.At(b, 1), 0); len(facts) != 1 {
		t.Errorf("Carl has %d facts, want the one from the retry", len(facts))
	}
	if used, _ := st.TokensThisMonth(ctx, u.ID); used != 330 {
		t.Errorf("tokens recorded = %d, want 330 (the empty reply counts too)", used)
	}
}

func TestKnownAtOffersPartialNames(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sid, _ := st.EnsureSeries(ctx, "Saga")
	chapters := []store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 19}, {Idx: 2, Title: "Two", FirstOrd: 20, LastOrd: 21}}
	var sentences []store.Sentence
	for i := 0; i < 20; i++ {
		sentences = append(sentences, store.Sentence{Ord: i, ChapterIdx: 1, Para: i, Text: "Someone spoke."})
	}
	sentences = append(sentences,
		store.Sentence{Ord: 20, ChapterIdx: 2, Para: 20, Text: "Guyen gave the order."},
		store.Sentence{Ord: 21, ChapterIdx: 2, Para: 21, Text: "The old sword had lain there for years."})
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "Saga One", SHA256: "a", SourcePath: "a"},
		chapters, sentences, CountChunks(chapters, sentences, 1000), 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.BookByID(ctx, bid)

	// Guyen and Lain are met first, then enough others that neither is
	// among the most recently active.
	news := []store.NewEntity{
		{Name: "Vrie Guyen", Type: "character", FirstOrd: 0, Facts: []store.FactRec{{Text: "Vrie Guyen commands the ship.", Ord: 0, Evidence: []int{0}}}},
		{Name: "Isa Lain", Type: "character", FirstOrd: 1, Facts: []store.FactRec{{Text: "Isa Lain is an engineer.", Ord: 1, Evidence: []int{1}}}},
	}
	for i := 0; i < recentEntities+1; i++ {
		news = append(news, store.NewEntity{Name: fmt.Sprintf("Crew %d", i), Type: "character", FirstOrd: 2 + i,
			Facts: []store.FactRec{{Text: "A crew member.", Ord: 2 + i, Evidence: []int{2 + i}}}})
	}
	if _, err := st.ApplyChunk(ctx, store.ChunkWrite{BookID: b.ID, SeriesID: sid, NextChunk: 1, New: news}); err != nil {
		t.Fatal(err)
	}

	chunks := buildChunks(chapters, sentences, 1000)
	x := &Extractor{Store: st}
	k, cands, _, err := x.knownAt(ctx, b, chunks[1])
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, c := range cands {
		offered = append(offered, c.Name)
	}
	if _, ok := k.cands[1]; !ok {
		t.Errorf("Vrie Guyen wasn't offered for a passage that says Guyen; offered %q", offered)
	}
	if _, ok := k.cands[2]; ok {
		t.Errorf("Isa Lain was offered for a passage that only says lain; offered %q", offered)
	}
}

func TestValidateIgnoresLeadingThe(t *testing.T) {
	c := chunk{Chapter: store.Chapter{Idx: 1, Title: "One"}, Part: 1, Parts: 1, Sentences: []store.Sentence{
		{Ord: 5, Para: 1, Text: "Ants swarmed the walls."},
		{Ord: 6, Para: 1, Text: "The drones fired."},
		{Ord: 7, Para: 2, Text: "Drones were cheap."},
	}}
	k := known{cands: map[int64]candidate{}, byName: map[string]int64{"ants": 9}}
	out := parse(t, `{"entities": [
		{"entity_id": "", "name": "the ants", "type": "species", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "The ants swarmed the walls.", "sentences": [1]}]},
		{"entity_id": "", "name": "the drones", "type": "item", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "The drones fired.", "sentences": [2]}]},
		{"entity_id": "", "name": "Drones", "type": "item", "is_narrator": false, "new_aliases": [],
		 "facts": [{"text": "Drones were cheap.", "sentences": [3]}]}
	], "time_markers": []}`)

	w, warns := validate(out, c, k)
	if len(w.Updates) != 1 || w.Updates[0].ID != 9 {
		t.Errorf("updates = %+v, want the ants filed under the known ants", w.Updates)
	}
	if len(w.New) != 1 || len(w.New[0].Facts) != 2 || w.New[0].FirstOrd != 6 {
		t.Errorf("new = %+v, want one drones entity first seen at 6 with both facts", w.New)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q", warns)
	}
}
