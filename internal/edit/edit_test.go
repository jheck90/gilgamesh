package edit

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/store"
)

func testNotes() store.Notes {
	ent := func(id int64, name, typ string, first int, factIDs ...int64) store.NoteEntity {
		e := store.NoteEntity{ID: id, Name: name, Type: typ, FirstBookID: 1, FirstIndex: 1, FirstOrd: first}
		for i, f := range factIDs {
			e.Facts = append(e.Facts, store.NoteFact{ID: f, Text: name + " fact", Ord: first + i})
		}
		return e
	}
	holsten := ent(7, "Holsten", "character", 0, 8)
	holsten.FirstBookID, holsten.FirstIndex, holsten.OtherFacts = 9, 0, 10
	holsten.Aliases = []store.NoteAlias{{ID: 2, Alias: "Sering's war", BookID: 1, BookIndex: 1, Ord: 20}, {ID: 3, Alias: "old alias", BookID: 9}}
	vrie := ent(1, "Vrie Guyen", "character", 10, 1, 2)
	vrie.Aliases = []store.NoteAlias{{ID: 1, Alias: "the commander", BookID: 1, BookIndex: 1, Ord: 12}}
	satellite := ent(12, "the satellite", "item", 85, 42)
	satellite.Aliases = []store.NoteAlias{{ID: 4, Alias: "Sentry Habitat", BookID: 1, BookIndex: 1, Ord: 90}}
	narrator := ent(8, "Narrator", "character", 1, 9)
	narrator.Narrator = true
	return store.Notes{
		Book:     store.Book{ID: 1, SeriesID: 1, SeriesIndex: 1, Title: "One"},
		Chapters: []store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 99}},
		Entities: []store.NoteEntity{
			holsten, narrator, ent(3, "Isa Lain", "character", 5, 4), vrie, ent(2, "Guyen", "character", 30, 3),
			ent(4, "the hooded man", "character", 40, 5), ent(5, "Mordecai", "character", 50, 6),
			ent(6, "the armour", "creature", 60, 7), ent(9, "Big Thing", "item", 70, 10, 11, 12, 13, 14, 15),
			ent(10, "Lone", "character", 80, 16, 17),
			ent(11, "Sentry Pod", "item", 2, 20, 21, 22, 23, 24, 25), satellite,
			ent(13, "the nanovirus", "term", 86, 43), ent(14, "uplift nanovirus", "term", 3, 30, 31, 32, 33, 34, 35),
			ent(15, "Eliza mode", "term", 4, 40), ent(16, "Eliza Kerns", "character", 87, 41),
		},
		Markers: []store.NoteMarker{{ID: 1, Label: "That night", Ord: 15}, {ID: 2, Label: "Year 1837", Ord: 16}},
	}
}

func TestCheck(t *testing.T) {
	raw := `{
		"category_map": [{"from": "creature", "to": "item"}, {"from": "bogus", "to": "item"}],
		"merges": [
			{"keep": "E1", "merge": ["E2"], "reason": "same commander"},
			{"keep": "E5", "merge": ["E4"], "reason": "the hooded man is Mordecai"},
			{"keep": "E3", "merge": ["E7"], "reason": "no"},
			{"keep": "E12", "merge": ["E11"], "reason": "the satellite is the Sentry Pod"},
			{"keep": "E13", "merge": ["E14"], "reason": "same virus"},
			{"keep": "E16", "merge": ["E15"], "reason": "Eliza's persona"}
		],
		"drop_entities": [{"id": "E6", "reason": "scenery"}, {"id": "E9", "reason": "x"}, {"id": "E8", "reason": "x"},
			{"id": "E7", "reason": "x"}, {"id": "E1", "reason": "x"}],
		"drop_aliases": [{"id": "A2", "reason": "wrong entity"}, {"id": "A3", "reason": "x"}],
		"drop_facts": ["F3", "F16", "F17", "F7", "F99"],
		"drop_markers": ["M1"]
	}`
	p, err := Check(testNotes(), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	e := p.Edit
	if !reflect.DeepEqual(e.CategoryMap, map[string]string{"creature": "item"}) {
		t.Errorf("category map = %v", e.CategoryMap)
	}
	// Guyen joins Vrie Guyen. Mordecai shares no word with the hooded man,
	// so that could be a reveal. The satellite's "Sentry" name comes after
	// it's met, so it can't justify the link either. Uplift nanovirus is
	// met first with 6 facts, too many to hide until the nanovirus shows
	// up; Eliza mode's single fact can wait for Eliza Kerns.
	if !reflect.DeepEqual(e.Merges, []store.Merge{{Into: 1, From: []int64{2}}, {Into: 16, From: []int64{15}}}) {
		t.Errorf("merges = %+v", e.Merges)
	}
	if !reflect.DeepEqual(e.DropEntity, []int64{6}) {
		t.Errorf("dropped entities = %v, want only the armour", e.DropEntity)
	}
	if !reflect.DeepEqual(e.DropAlias, []int64{2}) {
		t.Errorf("dropped aliases = %v", e.DropAlias)
	}
	// F16 stays as Lone's only fact; F7 goes with the armour anyway.
	if !reflect.DeepEqual(e.DropFact, []int64{3, 17}) {
		t.Errorf("dropped facts = %v, want [3 17]", e.DropFact)
	}
	if !reflect.DeepEqual(e.DropMarker, []int64{1}) {
		t.Errorf("dropped markers = %v", e.DropMarker)
	}
	if len(p.Merges) != 2 || !strings.HasPrefix(p.Merges[0], "Guyen (1 facts) into Vrie Guyen") {
		t.Errorf("merge description = %q", p.Merges)
	}
	// bogus category, Mordecai, Isa Lain, the satellite, uplift
	// nanovirus, Big Thing, narrator, Holsten, Vrie Guyen (in a merge),
	// the old alias, F99, Lone's first fact.
	if len(p.Rejected) != 12 {
		t.Errorf("%d rejected, want 12:\n%s", len(p.Rejected), strings.Join(p.Rejected, "\n"))
	}
}

func TestRenderUser(t *testing.T) {
	u := renderUser(testNotes())
	for _, want := range []string{
		"CATEGORIES IN USE: character (9), item (3), term (3), creature (1)",
		"E1 [character] Vrie Guyen, first seen ch1; also: A1 \"the commander\" (ch1)",
		"E7 [character] Holsten, from an earlier book with 10 facts there; also: A2 \"Sering's war\" (ch1), \"old alias\"",
		"E8 [character] Narrator, first seen ch1, narrator",
		"  F1 ch1: Vrie Guyen fact",
		"  M2 ch1: Year 1837",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("prompt is missing %q:\n%s", want, u)
		}
	}
}
