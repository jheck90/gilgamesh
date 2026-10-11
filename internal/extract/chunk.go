package extract

import (
	"strings"

	"github.com/jheck90/gilgamesh/internal/store"
)

// chunk is one extraction call: a chapter, or part of a long one, split on
// paragraph boundaries.
type chunk struct {
	Chapter   store.Chapter
	Part      int // 1-based
	Parts     int
	Sentences []store.Sentence // the model sees these numbered from 1
}

func (c chunk) firstOrd() int { return c.Sentences[0].Ord }

// sentence returns the sentence the model called number n.
func (c chunk) sentence(n int) (store.Sentence, bool) {
	if n < 1 || n > len(c.Sentences) {
		return store.Sentence{}, false
	}
	return c.Sentences[n-1], true
}

// buildChunks groups each chapter's paragraphs into chunks of about
// maxWords. A single paragraph longer than that gets a chunk to itself.
func buildChunks(chapters []store.Chapter, sentences []store.Sentence, maxWords int) []chunk {
	var out []chunk
	i := 0
	for _, ch := range chapters {
		var parts [][]store.Sentence
		var cur []store.Sentence
		words := 0
		for i < len(sentences) && sentences[i].Ord <= ch.LastOrd {
			// Gather one paragraph.
			j := i
			pw := 0
			for j < len(sentences) && sentences[j].Ord <= ch.LastOrd && sentences[j].Para == sentences[i].Para {
				pw += len(strings.Fields(sentences[j].Text))
				j++
			}
			if words > 0 && words+pw > maxWords {
				parts = append(parts, cur)
				cur, words = nil, 0
			}
			cur = append(cur, sentences[i:j]...)
			words += pw
			i = j
		}
		if len(cur) > 0 {
			parts = append(parts, cur)
		}
		for n, p := range parts {
			out = append(out, chunk{Chapter: ch, Part: n + 1, Parts: len(parts), Sentences: p})
		}
	}
	return out
}

// CountChunks reports how many extraction calls a book needs.
func CountChunks(chapters []store.Chapter, sentences []store.Sentence, maxWords int) int {
	return len(buildChunks(chapters, sentences, maxWords))
}
