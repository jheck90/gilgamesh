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
