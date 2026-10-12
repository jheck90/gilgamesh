// Package text splits book paragraphs into sentences, matches names
// inside them, and finds a snapped page among them.
package text

import (
	"strings"
	"unicode"
)

// Titles that end in a period but rarely end a sentence.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "st": true, "jr": true,
	"sr": true, "prof": true, "gen": true, "col": true, "lt": true, "sgt": true,
	"capt": true, "cpt": true, "maj": true, "cmdr": true, "adm": true,
	"gov": true, "sen": true, "rep": true, "rev": true, "hon": true, "mt": true,
	"vs": true, "e.g": true, "i.e": true, "approx": true, "fig": true,
}

// Split breaks a paragraph into sentences. It errs toward not splitting: a
// missed split only makes one evidence sentence longer, while a bad split
// can cut a name in half.
func Split(p string) []string {
	rs := []rune(p)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		if !isTerminal(rs[i]) {
			continue
		}
		end := i + 1
		for end < len(rs) && isTerminal(rs[end]) {
			end++
		}
		single := end-i == 1 && rs[i] == '.'
		for end < len(rs) && isCloser(rs[end]) {
			end++
		}
		if end < len(rs) && !unicode.IsSpace(rs[end]) {
			i = end - 1
			continue
		}
		next := end
		for next < len(rs) && unicode.IsSpace(rs[next]) {
			next++
		}
		if next < len(rs) && !startsSentence(rs[next]) {
			i = end - 1
			continue
		}
		if single && next < len(rs) && isAbbreviation(rs[start:i]) {
			i = end - 1
			continue
		}
		if s := strings.TrimSpace(string(rs[start:end])); s != "" {
			out = append(out, s)
		}
		start = end
		i = end - 1
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

func isTerminal(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '…' }

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', ')', ']', '»':
		return true
	}
	return false
}

func startsSentence(r rune) bool {
	if unicode.IsUpper(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '"', '\'', '“', '‘', '(', '[', '«', '—', '*':
		return true
	}
	return false
}

// isAbbreviation reports whether the text before a period ends in a title
// like "Mr" or a single capital initial like the "J" in "J. R. R.".
func isAbbreviation(before []rune) bool {
	j := len(before)
	for j > 0 && (unicode.IsLetter(before[j-1]) || before[j-1] == '.') {
		j--
	}
	word := before[j:]
	if len(word) == 1 && unicode.IsUpper(word[0]) {
		return true
	}
	return abbreviations[strings.ToLower(string(word))]
}
