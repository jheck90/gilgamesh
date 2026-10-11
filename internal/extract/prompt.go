package extract

import (
	"fmt"
	"strings"

	"github.com/jheck90/gilgamesh/internal/store"
)

const systemPrompt = `You are building a spoiler-safe reference for someone reading a book. You read the book one passage at a time, in order, and take notes on the people, groups, places, things and terms in the story. A note is shown to the reader only once they have read the sentences it cites, so every note must be supported by those sentences.

Rules:
- Use only the passage and the KNOWN ENTITIES list. Even if you recognize the book, never add anything from outside it: no later events, no hidden identities, no names the text hasn't used yet. Writing down something the reader hasn't read yet is the worst mistake you can make.
- Facts are plain statements of what the text says or shows: who or what something is, what it did, what happened to it, how it relates to others, what it can do. No interpretation, significance, foreshadowing, opinion or guesses.
- Write each fact as one short, self-contained sentence that names its subject, so it still makes sense on its own months later.
- Record only what is new. Skip anything already covered by the facts listed for a known entity, and skip trivial moments (walking, glancing, small talk) unless they change who someone is or what happened to them. Favor the details a reader would want back if they forgot who this was.
- Every fact lists the numbers of the sentences that support it.
- entity_id: the ID from KNOWN ENTITIES (like "E12") when the passage refers to that entity; an empty string for anything not in the list. Treat two names as the same entity only when the text itself makes that clear.
- name: for a new entity, the name as the passage writes it (the fullest form used). For a known entity, repeat its listed name.
- new_aliases: other names, titles or nicknames the passage uses for the entity, each with the number of a sentence where that exact wording appears. Only when the text makes clear it is the same entity.
- is_narrator: true only for a first-person narrator. If the narrator's name hasn't appeared in the text yet, name them "Narrator".
- type: a short lowercase category such as character, organization, place, item, creature, species, event or term. Reuse the categories already in use where they fit.
- time_markers: places where the passage states in-world time (a day count, date, year, season, countdown or similar), with the sentence number. Use the text's own wording, shortened, like "Day 3".
- Only include entities with something new in this passage. Empty lists are fine.`

var outputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"entities", "time_markers"},
	"properties": map[string]any{
		"entities": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"entity_id", "name", "type", "is_narrator", "new_aliases", "facts"},
				"properties": map[string]any{
					"entity_id":   map[string]any{"type": "string"},
					"name":        map[string]any{"type": "string"},
					"type":        map[string]any{"type": "string"},
					"is_narrator": map[string]any{"type": "boolean"},
					"new_aliases": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"alias", "sentence"},
							"properties": map[string]any{
								"alias":    map[string]any{"type": "string"},
								"sentence": map[string]any{"type": "integer"},
							},
						},
					},
					"facts": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"text", "sentences"},
							"properties": map[string]any{
								"text":      map[string]any{"type": "string"},
								"sentences": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
							},
						},
					},
				},
			},
		},
		"time_markers": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"label", "sentence"},
				"properties": map[string]any{
					"label":    map[string]any{"type": "string"},
					"sentence": map[string]any{"type": "integer"},
				},
			},
		},
	},
}

// output mirrors outputSchema.
type output struct {
	Entities []struct {
		EntityID   string `json:"entity_id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		IsNarrator bool   `json:"is_narrator"`
		NewAliases []struct {
			Alias    string `json:"alias"`
			Sentence int    `json:"sentence"`
		} `json:"new_aliases"`
		Facts []struct {
			Text      string `json:"text"`
			Sentences []int  `json:"sentences"`
		} `json:"facts"`
	} `json:"entities"`
	TimeMarkers []struct {
		Label    string `json:"label"`
		Sentence int    `json:"sentence"`
	} `json:"time_markers"`
}

// candidate is a known entity offered to the model for this chunk.
type candidate struct {
	store.EntitySummary
	Recent []string
}

func renderUser(b store.Book, c chunk, cands []candidate, types []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "BOOK: %s", b.Title)
	if b.SeriesName != b.Title {
		fmt.Fprintf(&sb, " (%s #%g)", b.SeriesName, b.SeriesIndex)
	}
	fmt.Fprintf(&sb, "\nSECTION: %s", c.Chapter.Title)
	if c.Parts > 1 {
		fmt.Fprintf(&sb, " (part %d of %d)", c.Part, c.Parts)
	}
	sb.WriteString("\n\nCATEGORIES IN USE: ")
	if len(types) == 0 {
		sb.WriteString("none yet")
	} else {
		sb.WriteString(strings.Join(types, ", "))
	}

	sb.WriteString("\n\nKNOWN ENTITIES (the only IDs you may use):\n")
	if len(cands) == 0 {
		sb.WriteString("none yet\n")
	}
	for _, e := range cands {
		fmt.Fprintf(&sb, "E%d [%s] %s", e.ID, e.Type, e.Name)
		if len(e.Aliases) > 0 {
			fmt.Fprintf(&sb, " (also: %s)", strings.Join(e.Aliases, "; "))
		}
		if e.Narrator {
			sb.WriteString(" (narrator)")
		}
		sb.WriteByte('\n')
		for _, f := range e.Recent {
			fmt.Fprintf(&sb, "  - %s\n", f)
		}
	}

	sb.WriteString("\nPASSAGE (sentences are numbered):\n")
	para := c.Sentences[0].Para
	for i, s := range c.Sentences {
		if s.Para != para {
			sb.WriteByte('\n')
			para = s.Para
		} else if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "[%d] %s", i+1, s.Text)
	}
	sb.WriteByte('\n')
	return sb.String()
}
