package edit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jheck90/gilgamesh/internal/store"
)

const systemPrompt = `You are the editor of a spoiler-safe reference for people reading a book. Another model read the book one passage at a time and took notes: entities (people, groups, places, things, terms), each with its facts in reading order, plus time markers. A reader opens an entry to remind themselves who or what something is, as of where they are. It is a glossary, not a plot summary.

Tidy the notes. You can only remove and combine; you cannot write anything new. Refer to everything by its ID.

- category_map: fold a category into a more common one that means the same (for example creature into species). Map only to a category already in use, and leave the main categories alone.
- merges: entities that are the same thing noted twice under forms of one name (the Iron Guild and Iron Guild; Rhee and Captain Ada Rhee). Each entity merged away must share a name word with the one it joins. Merge only when the notes make it plain: people who share a surname are often different people, and a name reused across generations belongs to a different individual.
- drop_entities: entries no reader would look up: incidental objects, scenery, unnamed walk-ons, a single moment. Keep anything named that a reader might wonder about, anything that recurs, and anything that matters to the story. Entities with more than 5 facts can't be dropped.
- drop_facts: an entry is a reference card, not a retelling of the plot. Keep what a reader would come back for: who or what it is, role and status, relationships, abilities and traits, beliefs and goals, and turning points that change its situation (captured, freed, maimed, promoted, killed, changed sides). Drop scene-level narration: single actions and reactions within a scene, passing injuries, movements, things said or asked in passing, and anything that repeats an earlier fact on the same entry (keep the earlier one). Long entries are usually mostly scene-level narration, so expect to drop many of their facts.
- drop_aliases: names attached to the wrong entity.
- drop_markers: time markers that don't place the story in time, like "That night", "Sunset" or "Hours later". Keep years, dates, day counts, ages and spans between events.

When unsure whether to drop an entity or merge two, keep them as they are. A short reason is enough.`

var outputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"category_map", "merges", "drop_entities", "drop_aliases", "drop_facts", "drop_markers"},
	"properties": map[string]any{
		"category_map": array(object(map[string]any{"from": str, "to": str})),
		"merges": array(object(map[string]any{
			"keep": str, "merge": map[string]any{"type": "array", "items": str}, "reason": str,
		})),
		"drop_entities": array(object(map[string]any{"id": str, "reason": str})),
		"drop_aliases":  array(object(map[string]any{"id": str, "reason": str})),
		"drop_facts":    map[string]any{"type": "array", "items": str},
		"drop_markers":  map[string]any{"type": "array", "items": str},
	},
}

var str = map[string]any{"type": "string"}

func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func object(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	return map[string]any{"type": "object", "additionalProperties": false, "required": req, "properties": props}
}

// reply mirrors outputSchema.
type reply struct {
	CategoryMap []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"category_map"`
	Merges []struct {
		Keep   string   `json:"keep"`
		Merge  []string `json:"merge"`
		Reason string   `json:"reason"`
	} `json:"merges"`
	DropEntities []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"drop_entities"`
	DropAliases []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"drop_aliases"`
	DropFacts   []string `json:"drop_facts"`
	DropMarkers []string `json:"drop_markers"`
}

// chapterOf labels positions by chapter number ("ch12"), which is all the
// editor needs to see order.
func chapterOf(chs []store.Chapter) func(ord int) string {
	return func(ord int) string {
		for i, c := range chs {
			if ord >= c.FirstOrd && ord <= c.LastOrd {
				return fmt.Sprintf("ch%d", i+1)
			}
		}
		return "ch?"
	}
}

func renderUser(n store.Notes) string {
	var sb strings.Builder
	ch := chapterOf(n.Chapters)
	fmt.Fprintf(&sb, "BOOK: %s", n.Book.Title)
	if n.Book.SeriesName != "" && n.Book.SeriesName != n.Book.Title {
		fmt.Fprintf(&sb, " (%s #%g)", n.Book.SeriesName, n.Book.SeriesIndex)
	}
	fmt.Fprintf(&sb, ", %d chapters\n\nCATEGORIES IN USE: ", len(n.Chapters))
	counts := map[string]int{}
	for _, e := range n.Entities {
		counts[e.Type]++
	}
	cats := make([]string, 0, len(counts))
	for c := range counts {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool {
		return counts[cats[i]] > counts[cats[j]] || counts[cats[i]] == counts[cats[j]] && cats[i] < cats[j]
	})
	for i, c := range cats {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s (%d)", c, counts[c])
	}

	sb.WriteString("\n\nENTITIES (in order of first appearance; facts in reading order):\n")
	for _, e := range n.Entities {
		fmt.Fprintf(&sb, "\nE%d [%s] %s", e.ID, e.Type, e.Name)
		if e.FirstBookID == n.Book.ID {
			fmt.Fprintf(&sb, ", first seen %s", ch(e.FirstOrd))
		} else {
			fmt.Fprintf(&sb, ", from an earlier book with %d facts there", e.OtherFacts)
		}
		if e.Narrator {
			sb.WriteString(", narrator")
		}
		for i, a := range e.Aliases {
			if i == 0 {
				sb.WriteString("; also: ")
			} else {
				sb.WriteString(", ")
			}
			if a.BookID == n.Book.ID {
				fmt.Fprintf(&sb, "A%d %q (%s)", a.ID, a.Alias, ch(a.Ord))
			} else {
				fmt.Fprintf(&sb, "%q", a.Alias)
			}
		}
		sb.WriteByte('\n')
		for _, f := range e.Facts {
			fmt.Fprintf(&sb, "  F%d %s: %s\n", f.ID, ch(f.Ord), f.Text)
		}
	}

	sb.WriteString("\nTIME MARKERS:\n")
	if len(n.Markers) == 0 {
		sb.WriteString("none\n")
	}
	for _, m := range n.Markers {
		fmt.Fprintf(&sb, "  M%d %s: %s\n", m.ID, ch(m.Ord), m.Label)
	}
	return sb.String()
}
