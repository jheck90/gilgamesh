package text

import (
	"strings"
	"unicode"
)

// Page is the run of sentences a snap matched, as indexes into the
// sentences searched: the ones holding the first and last words matched.
type Page struct {
	First, Last int
}

const (
	pageRun      = 3  // words matched together, in order
	pageMinWords = 4  // fewer words found than this is never a match
	pageMaxSkip  = 30 // words a match can skip between two runs, on either side
)

// MatchPage finds text among a book's sentences, given in reading order.
// It matches runs of consecutive words, so a word added, missed or
// misread here and there lowers the score rather than breaking the match.
// ok is false unless at least four words, and half of them, are found.
// When the text matches equally well in more than one place, the earliest
// wins.
func MatchPage(text string, sentences []string) (page Page, ok bool) {
	q := pageWords(text)         // the text's words
	starts := map[string][]int{} // each run of the text, and where it starts
	for i := 0; i+pageRun <= len(q); i++ {
		run := strings.Join(q[i:i+pageRun], " ")
		starts[run] = append(starts[run], i)
	}

	var book []string    // the book's words in order
	var sentenceOf []int // the sentence each one is in
	for i, s := range sentences {
		for _, w := range pageWords(s) {
			book = append(book, w)
			sentenceOf = append(sentenceOf, i)
		}
	}

	// A hit is a run of the text found in the book. Hits chain into a
	// match when they come in the same order in both, close together;
	// words counts the text's words the best chain ending here covers.
	type hit struct{ q, b, words, prev int }
	var hits []hit
	for b := 0; b+pageRun <= len(book); b++ {
		for _, i := range starts[strings.Join(book[b:b+pageRun], " ")] {
			hits = append(hits, hit{q: i, b: b, words: pageRun, prev: -1})
		}
	}
	best := -1
	for k := range hits {
		h := &hits[k]
		for j := k - 1; j >= 0 && h.b-hits[j].b <= pageMaxSkip+pageRun; j-- {
			p := hits[j]
			dq, db := h.q-p.q, h.b-p.b
			if dq <= 0 || db <= 0 || dq > pageMaxSkip+pageRun {
				continue
			}
			// Runs that overlap must overlap the same way in both.
			if (dq < pageRun || db < pageRun) && dq != db {
				continue
			}
			if n := p.words + min(dq, pageRun); n > h.words {
				h.words, h.prev = n, j
			}
		}
		if best < 0 || h.words > hits[best].words {
			best = k
		}
	}
	if best < 0 || hits[best].words < pageMinWords || 2*hits[best].words < len(q) {
		return Page{}, false
	}
	first := best
	for hits[first].prev >= 0 {
		first = hits[first].prev
	}
	return Page{First: sentenceOf[hits[first].b], Last: sentenceOf[hits[best].b+pageRun-1]}, true
}

// pageWords splits text into the words pages are matched on, folded like
// names.
func pageWords(s string) []string {
	return strings.FieldsFunc(Fold(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
