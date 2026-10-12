package text

import (
	"regexp"
	"strings"
	"unicode"
)

// Page is the run of sentences a snap matched, as indexes into the
// sentences searched. Last holds the last word matched. First holds the
// first, or an earlier sentence if the text has words before the match:
// those may be the book's, misread.
type Page struct {
	First, Last int
	// Cut is true when the text stops partway through sentence Last, as a
	// photo does when a sentence runs on to the next page.
	Cut bool
	// Matched of the text's Words were found in the book, in order: the
	// match's score. Both are set whether or not it's a match.
	Matched, Words int
}

const (
	pageRun      = 3  // words matched together, in order
	pageMinWords = 4  // fewer words found than this is never a match
	pageMaxSkip  = 30 // words a match can skip between two runs, on either side
)

// MatchPage finds text among a book's sentences, given in reading order.
// It matches runs of consecutive words, so a word added, missed or
// misread here and there lowers the score rather than breaking the match,
// and joins words split by a hyphen at the end of a line.
// ok is false unless at least four words, and half of them, are found.
// When the text matches equally well in more than one place, the earliest
// wins.
func MatchPage(text string, sentences []string) (page Page, ok bool) {
	text = lineHyphen.ReplaceAllString(text, "$1$2")
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
	matched := 0
	if best >= 0 {
		matched = hits[best].words
	}
	if matched < pageMinWords || 2*matched < len(q) {
		return Page{Matched: matched, Words: len(q)}, false
	}
	first := best
	for hits[first].prev >= 0 {
		first = hits[first].prev
	}
	end := hits[best].b + pageRun // the book word after the last one matched
	last := sentenceOf[end-1]
	more := q[hits[best].q+pageRun:] // the text's words after it
	// The last words on a page can be misread too, so the rest of the
	// sentence counts as shown if the text goes on as far, with at least
	// half of those words read right.
	rest, right := 0, 0
	for ; end+rest < len(book) && sentenceOf[end+rest] == last; rest++ {
		if rest < len(more) && more[rest] == book[end+rest] {
			right++
		}
	}
	// Words before the match may be the end of an earlier sentence,
	// misread (or a running head). Count them as the book's, so a page
	// never starts later than it might.
	start := max(0, hits[first].b-min(hits[first].q, pageMaxSkip))
	return Page{
		First:   sentenceOf[start],
		Last:    last,
		Cut:     rest > len(more) || 2*right < rest,
		Matched: matched,
		Words:   len(q),
	}, true
}

// lineHyphen finds a word split by a hyphen at the end of a line.
var lineHyphen = regexp.MustCompile(`(\pL)[-‐][ \t]*\r?\n\s*(\pL)`)

// pageWords splits text into the words pages are matched on, folded like
// names.
func pageWords(s string) []string {
	return strings.FieldsFunc(Fold(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
