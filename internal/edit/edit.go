// Package edit tidies a book's notes once extraction has finished: a model
// reads all of them at once and says what to drop or combine.
//
// Unlike extraction, the editor sees the whole book, so it is only allowed
// to remove and combine, never to write: Check enforces that here and the
// store enforces it again. Removing can't reveal anything. Combining keeps
// the kept entry's first appearance and never shows a merged note earlier
// than before, and it's limited to entities whose names share a word, so
// a twist ("the hooded man was Mordecai") can't be merged at all.
package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

// maxDropFacts is the most facts an entity can have and still be dropped
// whole; anything bigger is a judgment call worth keeping.
const maxDropFacts = 5

type Editor struct {
	LLM llm.Client
}

// Ask sends the notes to the model and returns its raw reply.
func (x *Editor) Ask(ctx context.Context, n store.Notes) ([]byte, llm.Usage, error) {
	return x.LLM.JSON(ctx, llm.Request{System: systemPrompt, User: renderUser(n), SchemaName: "notes_edit", Schema: outputSchema})
}

// Plan is a checked edit, with what it does in readable form.
type Plan struct {
	Edit     store.Edit
	Retype   []string
	Merges   []string
	Titles   []string
	Entities []string
	Aliases  []string
	Facts    []string
	Markers  []string
	Rejected []string // parts of the reply that broke the rules, and why
}

// Check turns a reply into an edit of n's book, leaving out anything the
// rules don't allow.
func Check(n store.Notes, raw []byte) (Plan, error) {
	var r reply
	if err := json.Unmarshal(raw, &r); err != nil {
		return Plan{}, fmt.Errorf("edit: reply wasn't valid JSON: %w", err)
	}
	p := Plan{Edit: store.Edit{BookID: n.Book.ID, SeriesID: n.Book.SeriesID, CategoryMap: map[string]string{}}}
	reject := func(format string, args ...any) { p.Rejected = append(p.Rejected, fmt.Sprintf(format, args...)) }
	ch := chapterOf(n.Chapters)

	ents := map[int64]*store.NoteEntity{}
	type factRef struct {
		f store.NoteFact
		e *store.NoteEntity
	}
	facts := map[int64]factRef{}
	type aliasRef struct {
		a store.NoteAlias
		e *store.NoteEntity
	}
	aliases := map[int64]aliasRef{}
	cats := map[string]bool{}
	for i := range n.Entities {
		e := &n.Entities[i]
		ents[e.ID] = e
		cats[e.Type] = true
		for _, f := range e.Facts {
			facts[f.ID] = factRef{f, e}
		}
		for _, a := range e.Aliases {
			aliases[a.ID] = aliasRef{a, e}
		}
	}
	markers := map[int64]store.NoteMarker{}
	for _, m := range n.Markers {
		markers[m.ID] = m
	}
	entity := func(ref string) (*store.NoteEntity, bool) {
		id, ok := parseID(ref, 'E')
		e := ents[id]
		return e, ok && e != nil
	}

	froms := map[string]bool{}
	for _, c := range r.CategoryMap {
		froms[strings.ToLower(strings.TrimSpace(c.From))] = true
	}
	for _, c := range r.CategoryMap {
		from, to := strings.ToLower(strings.TrimSpace(c.From)), strings.ToLower(strings.TrimSpace(c.To))
		switch {
		case !cats[from] || !cats[to] || from == to:
			reject("category %q to %q: both must be categories in use", c.From, c.To)
		case froms[to]:
			reject("category %q to %q: %q is itself being folded away", c.From, c.To, c.To)
		default:
			p.Edit.CategoryMap[from] = to
			p.Retype = append(p.Retype, from+" → "+to)
		}
	}

	// into maps every entity merged away to the one it joins.
	into := map[int64]int64{}
	inMerge := map[int64]bool{}
	for _, m := range r.Merges {
		var group []*store.NoteEntity
		ok := true
		for _, ref := range append([]string{m.Keep}, m.Merge...) {
			e, found := entity(ref)
			if !found || inMerge[e.ID] {
				reject("merge %s with %v: %s is unknown or already in another merge", m.Keep, m.Merge, ref)
				ok = false
				break
			}
			if len(group) == 0 || !containsEntity(group, e.ID) {
				group = append(group, e)
			}
		}
		if !ok || len(group) < 2 {
			continue
		}
		keep := group[0]
		var from []int64
		for _, e := range group[1:] {
			// The merged entry shows both names once both have appeared,
			// so the word they share must be visible by then.
			keepLater := keep.FirstBookID == n.Book.ID && keep.FirstOrd > e.FirstOrd
			link := e.FirstOrd
			if keepLater {
				link = keep.FirstOrd
			}
			switch {
			case e.FirstBookID != n.Book.ID:
				reject("merge %s into %s: %s was met in an earlier book", e.Name, keep.Name, e.Name)
			case !shareWord(e.Name, visibleNames(keep, n.Book, link)):
				reject("merge %s into %s: the names share no word, so it could be a reveal", e.Name, keep.Name)
			case keepLater && len(e.Facts) > maxDropFacts:
				// Its notes would be hidden until keep appears, which for
				// anything bigger is worse than leaving the duplicate.
				reject("merge %s into %s: %s comes first and has %d facts, which would wait until %s appears", e.Name, keep.Name, e.Name, len(e.Facts), ch(keep.FirstOrd))
			default:
				from = append(from, e.ID)
				into[e.ID] = keep.ID
				inMerge[e.ID] = true
				p.Merges = append(p.Merges, fmt.Sprintf("%s (%d facts) into %s: %s", e.Name, len(e.Facts), keep.Name, m.Reason))
			}
		}
		if len(from) > 0 {
			inMerge[keep.ID] = true
			p.Edit.Merges = append(p.Edit.Merges, store.Merge{Into: keep.ID, From: from})
		}
	}

	dropped := map[int64]bool{}
	for _, d := range r.DropEntities {
		e, found := entity(d.ID)
		switch {
		case !found:
			reject("drop entity %s: unknown", d.ID)
		case inMerge[e.ID]:
			reject("drop %s: it's part of a merge", e.Name)
		case e.FirstBookID != n.Book.ID:
			reject("drop %s: it was met in an earlier book", e.Name)
		case e.Narrator:
			reject("drop %s: it's the narrator", e.Name)
		case len(e.Facts)+e.OtherFacts > maxDropFacts:
			reject("drop %s: it has %d facts, more than %d", e.Name, len(e.Facts)+e.OtherFacts, maxDropFacts)
		case !dropped[e.ID]:
			dropped[e.ID] = true
			p.Edit.DropEntity = append(p.Edit.DropEntity, e.ID)
			p.Entities = append(p.Entities, fmt.Sprintf("%s [%s] (%d facts): %s", e.Name, e.Type, len(e.Facts), d.Reason))
		}
	}

	// Titles come after drops are known, so a title can't be an alias
	// that's being removed or belong to an entry that's going away.
	droppingAlias := map[int64]bool{}
	for _, d := range r.DropAliases {
		id, _ := parseID(d.ID, 'A')
		droppingAlias[id] = true
	}
	titled := map[int64]bool{}
	for _, t := range r.Titles {
		e, found := entity(t.ID)
		aid, _ := parseID(t.Alias, 'A')
		a, aliasFound := aliases[aid]
		switch {
		case !found || !aliasFound:
			reject("title %s for %s: unknown", t.Alias, t.ID)
		case dropped[e.ID] || into[e.ID] != 0:
			reject("title for %s: it's being dropped or merged away", e.Name)
		case a.e.ID != e.ID && into[a.e.ID] != e.ID:
			reject("title %q for %s: that name belongs to %s", a.a.Alias, e.Name, a.e.Name)
		case droppingAlias[aid]:
			reject("title %q for %s: that alias is being dropped", a.a.Alias, e.Name)
		case !titled[e.ID]:
			titled[e.ID] = true
			p.Edit.Titles = append(p.Edit.Titles, store.Title{Entity: e.ID, Alias: aid})
			p.Titles = append(p.Titles, fmt.Sprintf("%s → %s (from %s)", e.Name, a.a.Alias, ch(a.a.Ord)))
		}
	}

	for _, d := range r.DropAliases {
		id, _ := parseID(d.ID, 'A')
		a, found := aliases[id]
		switch {
		case !found:
			reject("drop alias %s: unknown", d.ID)
		case a.a.BookID != n.Book.ID:
			reject("drop alias %q: it was given in an earlier book", a.a.Alias)
		case !dropped[a.e.ID] && !containsID(p.Edit.DropAlias, id):
			p.Edit.DropAlias = append(p.Edit.DropAlias, id)
			p.Aliases = append(p.Aliases, fmt.Sprintf("%q from %s: %s", a.a.Alias, a.e.Name, d.Reason))
		}
	}

	// Facts are grouped by the entry they'll end up on, so no entry that
	// stays is left with nothing: its first fact is kept.
	dropFact := map[int64]bool{}
	for _, ref := range r.DropFacts {
		id, _ := parseID(ref, 'F')
		f, found := facts[id]
		switch {
		case !found:
			reject("drop fact %s: unknown", ref)
		case !dropped[f.e.ID]:
			dropFact[id] = true
		}
	}
	final := func(id int64) int64 {
		if k, ok := into[id]; ok {
			return k
		}
		return id
	}
	firstOf := map[int64]store.NoteFact{}
	left := map[int64]int{}
	for _, e := range n.Entities {
		if dropped[e.ID] {
			continue
		}
		k := final(e.ID)
		for _, f := range e.Facts {
			if cur, ok := firstOf[k]; !ok || f.Ord < cur.Ord {
				firstOf[k] = f
			}
			if !dropFact[f.ID] {
				left[k]++
			}
		}
	}
	for k, f := range firstOf {
		if left[k] == 0 && ents[k].OtherFacts == 0 && dropFact[f.ID] {
			delete(dropFact, f.ID)
			reject("kept %s's first fact so its entry isn't empty", ents[k].Name)
		}
	}
	for _, e := range n.Entities {
		for _, f := range e.Facts {
			if dropFact[f.ID] {
				p.Edit.DropFact = append(p.Edit.DropFact, f.ID)
				p.Facts = append(p.Facts, fmt.Sprintf("%s, %s: %s", e.Name, ch(f.Ord), f.Text))
			}
		}
	}

	for _, ref := range r.DropMarkers {
		id, _ := parseID(ref, 'M')
		m, found := markers[id]
		switch {
		case !found:
			reject("drop marker %s: unknown", ref)
		case !containsID(p.Edit.DropMarker, id):
			p.Edit.DropMarker = append(p.Edit.DropMarker, id)
			p.Markers = append(p.Markers, fmt.Sprintf("%s: %s", ch(m.Ord), m.Label))
		}
	}
	return p, nil
}

// visibleNames are e's names a reader of book b has seen by sentence ord.
func visibleNames(e *store.NoteEntity, b store.Book, ord int) []string {
	out := []string{e.Name}
	for _, a := range e.Aliases {
		if a.BookIndex < b.SeriesIndex || a.BookID == b.ID && a.Ord <= ord {
			out = append(out, a.Alias)
		}
	}
	return out
}

// genericWords can't by themselves show two names are the same thing.
var genericWords = map[string]bool{
	"that": true, "this": true, "with": true, "from": true, "their": true, "these": true, "those": true,
	"into": true, "other": true, "some": true, "doctor": true, "chief": true, "commander": true,
	"captain": true, "general": true, "lord": true, "lady": true, "king": true, "queen": true,
	"prince": true, "princess": true, "mister": true, "miss": true,
}

// shareWord reports whether name has a word of four or more letters, other
// than a title or filler, in common with any of others.
func shareWord(name string, others []string) bool {
	have := map[string]bool{}
	for _, o := range others {
		for _, w := range words(o) {
			have[w] = true
		}
	}
	for _, w := range words(name) {
		if have[w] {
			return true
		}
	}
	return false
}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(text.Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 4 && !genericWords[w] {
			out = append(out, w)
		}
	}
	return out
}

func parseID(ref string, kind byte) (int64, bool) {
	ref = strings.ToUpper(strings.TrimSpace(ref))
	if len(ref) < 2 || ref[0] != kind {
		return 0, false
	}
	id, err := strconv.ParseInt(ref[1:], 10, 64)
	return id, err == nil
}

func containsEntity(es []*store.NoteEntity, id int64) bool {
	for _, e := range es {
		if e.ID == id {
			return true
		}
	}
	return false
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
