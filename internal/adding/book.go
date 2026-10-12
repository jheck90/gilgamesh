// Package adding turns an EPUB into a stored book and takes notes on it,
// either from the command line or in the background, one book at a time.
package adding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

// Sections shorter than this are title pages and separators, not chapters.
const minSectionWords = 20

// Section is one section of the EPUB as `ingest --dry-run` lists it.
type Section struct {
	N       int
	Title   string
	Words   int
	Skipped string // why it's left out, or ""
}

// Prepare numbers every sentence in reading order and turns sections into
// chapters. A chapter keeps its section number, so the numbers --dry-run
// shows stay the chapter numbers even when some sections are skipped.
func Prepare(b *epub.Book, skip map[int]bool) ([]store.Chapter, []store.Sentence, []Section) {
	var chapters []store.Chapter
	var sentences []store.Sentence
	var plan []Section
	para := 0
	for i, sec := range b.Sections {
		n := i + 1
		var sents []store.Sentence
		words := 0
		for _, p := range sec.Paragraphs {
			for _, s := range text.Split(p) {
				sents = append(sents, store.Sentence{Ord: len(sentences) + len(sents), ChapterIdx: n, Para: para, Text: s})
				words += len(strings.Fields(s))
			}
			para++
		}
		title := sec.Title
		if title == "" {
			title = fmt.Sprintf("Section %d", n)
		}
		p := Section{N: n, Title: title, Words: words}
		switch {
		case skip[n]:
			p.Skipped = "--skip"
		case words < minSectionWords:
			p.Skipped = "too short"
		}
		plan = append(plan, p)
		if p.Skipped != "" {
			continue
		}
		chapters = append(chapters, store.Chapter{Idx: n, Title: title, FirstOrd: sents[0].Ord, LastOrd: sents[len(sents)-1].Ord})
		sentences = append(sentences, sents...)
	}
	return chapters, sentences, plan
}

// ParseSkip reads a list of sections like "1-3,40".
func ParseSkip(s string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := a, error(nil)
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b < a {
			return nil, fmt.Errorf("can't read --skip %q", part)
		}
		for i := a; i <= b; i++ {
			out[i] = true
		}
	}
	return out, nil
}

// SeriesOf picks the series a book goes in: the one asked for, else the
// EPUB's, else a series of its own.
func SeriesOf(b *epub.Book, name string, index float64) (string, float64) {
	if name == "" {
		name = b.Series
	}
	if name == "" {
		name = b.Title
	}
	if index == 0 {
		index = b.SeriesIndex
	}
	if index == 0 {
		index = 1
	}
	return name, index
}

func FileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RunConfig sets the model a run uses on a key: the one asked for, else the
// key's own, else (on Anthropic or Claude Code) the command's default.
func RunConfig(c llm.Config, model, anthropicDefault, effort string) llm.Config {
	if model != "" {
		c.Model = model
	}
	if p := c.Provider; p == llm.ProviderAnthropic || p == llm.ProviderClaudeCode {
		if c.Model == "" {
			c.Model = anthropicDefault
		}
		c.Effort = effort
	}
	return c
}

// Backup copies the database at dbPath to a new file beside it before
// book bookID is edited, and returns the copy's path.
func Backup(ctx context.Context, st *store.Store, dbPath string, bookID int64) (string, error) {
	base := fmt.Sprintf("%s.before-edit-%d-%s", dbPath, bookID, time.Now().Format("20060102-150405"))
	backup := base
	for i := 2; ; i++ {
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			break
		}
		backup = fmt.Sprintf("%s-%d", base, i)
	}
	if err := st.Backup(ctx, backup); err != nil {
		return "", fmt.Errorf("backing up before the edit: %w", err)
	}
	return backup, nil
}
