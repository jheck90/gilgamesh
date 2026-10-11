package extract

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

// known is what validation needs about the entities a reader has met
// before this chunk.
type known struct {
	cands  map[int64]candidate // offered to the model this chunk
	byName map[string]int64    // text.Key of a name or alias -> id, all visible entities
}

// validate turns model output into rows, dropping anything the passage
// doesn't support. This is where spoiler safety is enforced; the prompt
// only asks nicely.
//
//   - Facts and aliases must cite sentences inside this chunk, so nothing
//     lands before the reader could have read it.
//   - An alias must appear verbatim in the passage, and is revealed no
//     earlier than its first appearance or the sentence it cites.
//   - A new entity's name must appear in the passage, so the model can't
//     name someone by a name the book hasn't revealed yet.
//   - A known entity only gets facts if one of its names already known to
//     the reader (or a new alias proven above) appears in the passage, so
//     the model can't quietly tie this passage to someone the reader
//     doesn't yet know it's about. The narrator is exempt, since a
//     first-person narrator is rarely named.
func validate(out output, c chunk, k known) (store.ChunkWrite, []string) {
	var w store.ChunkWrite
	var warn []string
	warnf := func(format string, args ...any) { warn = append(warn, fmt.Sprintf(format, args...)) }

	updates := map[int64]*store.EntityUpdate{}
	var updateOrder []int64
	news := map[string]*store.NewEntity{}
	var newOrder []string

	for _, e := range out.Entities {
		label := e.Name
		if e.EntityID != "" {
			label = e.EntityID + " " + e.Name
		}

		var facts []store.FactRec
		for _, f := range e.Facts {
			t := clean(f.Text)
			ords, ok := c.ords(f.Sentences)
			if t == "" || !ok {
				warnf("%s: dropped fact citing sentences %v outside this passage: %q", label, f.Sentences, f.Text)
				continue
			}
			facts = append(facts, store.FactRec{Text: t, Ord: ords[len(ords)-1], Evidence: ords})
		}
		var aliases []store.AliasRec
		for _, a := range e.NewAliases {
			alias := clean(a.Alias)
			s, ok := c.sentence(a.Sentence)
			first, found := c.firstMention(alias)
			if alias == "" || !ok || !found {
				warnf("%s: dropped alias %q: not in this passage", label, a.Alias)
				continue
			}
			// The model sometimes cites the wrong sentence. Dropping the
			// alias would let a later passage mistake it for someone new,
			// so reveal it once the reader has both seen the name and
			// reached the sentence the model cited.
			aliases = append(aliases, store.AliasRec{Alias: alias, Ord: max(s.Ord, first)})
		}

		id, isKnown := int64(0), false
		if e.EntityID != "" {
			n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(e.EntityID)), "E"), 10, 64)
			if _, offered := k.cands[n]; err != nil || !offered {
				warnf("%s: dropped, not one of the offered entity IDs", label)
				continue
			}
			id, isKnown = n, true
		} else if n, ok := k.byName[text.Key(e.Name)]; ok {
			id, isKnown = n, true // the model didn't spot a known name
		}

		if isKnown {
			narrator := false
			names := []string{e.Name}
			if cand, ok := k.cands[id]; ok {
				narrator = cand.Narrator
				names = append([]string{cand.Name}, cand.Aliases...)
			}
			if !narrator && !c.mentionsAny(names) && len(aliases) == 0 {
				warnf("%s: dropped %d facts, none of its known names appear in this passage", label, len(facts))
				continue
			}
			u, ok := updates[id]
			if !ok {
				u = &store.EntityUpdate{ID: id}
				updates[id] = u
				updateOrder = append(updateOrder, id)
			}
			u.SetNarrator = u.SetNarrator || (e.IsNarrator && !narrator)
			u.Aliases = append(u.Aliases, aliases...)
			u.Facts = append(u.Facts, facts...)
			continue
		}

		name := clean(e.Name)
		first, found := c.firstMention(name)
		if !found && e.IsNarrator && strings.EqualFold(name, store.NarratorName) {
			first, found = c.firstOrd(), true
		}
		if !found && len(aliases) > 0 {
			warnf("%s: name isn't in the passage, using alias %q instead", label, aliases[0].Alias)
			name, first, found = aliases[0].Alias, aliases[0].Ord, true
			aliases = aliases[1:]
		}
		if !found {
			warnf("%s: dropped new entity, its name doesn't appear in this passage", label)
			continue
		}
		if len(facts) == 0 && len(aliases) == 0 {
			continue
		}
		key := text.Key(name)
		ne, ok := news[key]
		if !ok {
			ne = &store.NewEntity{Name: name, Type: normType(e.Type), Narrator: e.IsNarrator, FirstOrd: first}
			news[key] = ne
			newOrder = append(newOrder, key)
		}
		ne.Aliases = append(ne.Aliases, aliases...)
		ne.Facts = append(ne.Facts, facts...)
	}

	for _, key := range newOrder {
		w.New = append(w.New, *news[key])
	}
	for _, id := range updateOrder {
		if u := updates[id]; len(u.Facts) > 0 || len(u.Aliases) > 0 || u.SetNarrator {
			w.Updates = append(w.Updates, *u)
		}
	}
	for _, m := range out.TimeMarkers {
		s, ok := c.sentence(m.Sentence)
		if l := clean(m.Label); l != "" && ok {
			w.Markers = append(w.Markers, store.MarkerRec{Label: l, Ord: s.Ord})
		} else {
			warnf("dropped time marker %q at sentence %d", m.Label, m.Sentence)
		}
	}
	return w, warn
}

// ords maps the model's sentence numbers to book ords, sorted. ok is false
// if the list is empty or any number is outside the chunk.
func (c chunk) ords(nums []int) ([]int, bool) {
	if len(nums) == 0 {
		return nil, false
	}
	seen := map[int]bool{}
	var out []int
	for _, n := range nums {
		s, ok := c.sentence(n)
		if !ok {
			return nil, false
		}
		if !seen[s.Ord] {
			seen[s.Ord] = true
			out = append(out, s.Ord)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, true
}

// firstMention finds where name first appears, ignoring a leading "the",
// so "the ants" is found in "Ants swarmed".
func (c chunk) firstMention(name string) (int, bool) {
	name = text.Key(name)
	for _, s := range c.Sentences {
		if text.ContainsName(s.Text, name) {
			return s.Ord, true
		}
	}
	return 0, false
}

func (c chunk) mentionsAny(names []string) bool {
	for _, n := range names {
		if _, ok := c.firstMention(n); ok {
			return true
		}
	}
	return false
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func normType(t string) string {
	t = strings.ToLower(clean(t))
	if t == "" {
		return "term"
	}
	return t
}
