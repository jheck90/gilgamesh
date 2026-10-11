package text

import (
	"strings"
	"unicode"
)

var foldReplacer = strings.NewReplacer(
	"’", "'", "‘", "'", "“", `"`, "”", `"`, " ", " ", "­", "",
)

// Fold normalizes text for name matching: lowercase, straight quotes,
// single spaces.
func Fold(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(foldReplacer.Replace(s))), " ")
}

// ContainsName reports whether name appears in s as whole words. Both are
// folded first, so "Princess Donut" matches "princess donut’s".
func ContainsName(s, name string) bool {
	return containsFolded(Fold(s), Fold(name))
}

// Key is the form names are compared in: folded, without a leading
// "the", so "the Messenger" and "Messenger" are the same name. "A" and
// "an" stay, since "a male" is rarely the male met earlier.
func Key(name string) string {
	return strings.TrimPrefix(Fold(name), "the ")
}

// notNameWords are capitalized words that can't stand in for a name on
// their own: titles, and words that often start a sentence.
var notNameWords = map[string]bool{
	"The": true, "And": true, "For": true, "But": true, "With": true, "From": true,
	"Doctor": true, "Chief": true, "Commander": true, "Captain": true, "General": true,
	"Professor": true, "Mister": true, "Miss": true, "Mrs": true, "Lord": true, "Lady": true,
	"Sir": true, "King": true, "Queen": true, "Prince": true, "Princess": true, "Saint": true,
	"Old": true, "New": true, "Great": true, "First": true, "Last": true,
}

// NameWords returns the words of names that could stand for the whole name
// on their own: capitalized, at least three letters, and not a title.
// "Doctor Vrie Guyen" gives Vrie and Guyen.
func NameWords(names ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		for _, w := range strings.FieldsFunc(foldReplacer.Replace(n), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
		}) {
			w = strings.TrimSuffix(w, "'s")
			rs := []rune(w)
			if len(rs) < 3 || !unicode.IsUpper(rs[0]) || notNameWords[w] || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// ContainsWord reports whether word appears in s as a whole word. Case
// counts, so "Lain" isn't found in "had lain there".
func ContainsWord(s, word string) bool {
	return containsFolded(foldReplacer.Replace(s), word)
}

func containsFolded(s, name string) bool {
	if name == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(name)
		if boundaryBefore(s, i) && boundaryAfter(s, end) {
			return true
		}
		from = i + 1
	}
}

func boundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r := lastRune(s[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func boundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r := []rune(s[i:])[0]
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func lastRune(s string) rune {
	rs := []rune(s)
	return rs[len(rs)-1]
}
