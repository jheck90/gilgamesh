// Package library lists the EPUB files an instance can add books from: the
// Library, a directory (usually mounted read-only) that the library page
// browses. A file there isn't a book until it's added.
package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jheck90/gilgamesh/internal/adding"
	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/store"
)

type Library struct {
	Root  string
	Store *store.Store // caches what each file holds
	// Now is the clock that decides which files are still arriving; nil
	// means time.Now.
	Now func() time.Time
}

// File is one EPUB in the library.
type File struct {
	Path        string // relative to the root, with forward slashes
	Abs         string // where it really is, symlinks resolved
	SHA256      string
	Title       string
	Author      string
	Series      string // from the EPUB; "" if it names none
	SeriesIndex float64
	// Arriving is set for a file changed in the last minute, which may
	// still be being written. Nothing else is known about it yet.
	Arriving bool
	// Unreadable says why the file can't be added, or is "".
	Unreadable string
}

// Scan lists every EPUB under the root. Only files that are new, or whose
// size or modification time changed, are read; the rest come from the
// cache in the store.
func (l *Library) Scan(ctx context.Context) ([]File, error) {
	cached, err := l.Store.LibraryFiles(ctx)
	if err != nil {
		return nil, err
	}
	var out []File
	err = filepath.WalkDir(l.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isEPUB(path) {
			return nil
		}
		rel, err := filepath.Rel(l.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := l.file(ctx, rel, path, cached)
		if errors.Is(err, ErrNotInLibrary) {
			return nil
		}
		if err != nil {
			return err
		}
		delete(cached, rel)
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for gone := range cached {
		if err := l.Store.DeleteLibraryFile(ctx, gone); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ErrNotInLibrary is returned for a path that isn't an EPUB inside the
// root: one with "..", or a symlink that leads out, or no such file.
var ErrNotInLibrary = errors.New("that isn't a book in the library")

// File describes the EPUB at rel, a path relative to the root as Scan
// lists it.
func (l *Library) File(ctx context.Context, rel string) (File, error) {
	if rel == "" || filepath.IsAbs(rel) || slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") || !isEPUB(rel) {
		return File{}, ErrNotInLibrary
	}
	cached, err := l.Store.LibraryFiles(ctx)
	if err != nil {
		return File{}, err
	}
	return l.file(ctx, rel, filepath.Join(l.Root, filepath.FromSlash(rel)), cached)
}

// file describes the file at path, rel under the root, reading it unless
// cached already holds it as it is now.
func (l *Library) file(ctx context.Context, rel, path string, cached map[string]store.LibraryFile) (File, error) {
	abs, err := l.inside(path)
	if err != nil {
		return File{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return File{}, ErrNotInLibrary
	}
	if l.now().Sub(info.ModTime()) < settle {
		return File{Path: rel, Abs: abs, Arriving: true}, nil
	}
	c, ok := cached[rel]
	if !ok || c.Size != info.Size() || c.ModTime != info.ModTime().UnixNano() {
		c = store.LibraryFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if err := read(path, &c); err != nil {
			return File{}, err
		}
		if err := l.Store.PutLibraryFile(ctx, c); err != nil {
			return File{}, err
		}
	}
	return File{Path: rel, Abs: abs, SHA256: c.SHA256, Title: c.Title, Author: c.Author, Series: c.Series, SeriesIndex: c.SeriesIndex, Unreadable: c.Unreadable}, nil
}

// inside resolves symlinks in path, refusing it if it leads out of the
// root.
func (l *Library) inside(path string) (string, error) {
	root, err := filepath.EvalSymlinks(l.Root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrNotInLibrary
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrNotInLibrary
	}
	return abs, nil
}

// A file changed more recently than this may still be being written, by
// Shelfmark say, so it can't be picked yet.
const settle = time.Minute

func (l *Library) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func isEPUB(path string) bool { return strings.EqualFold(filepath.Ext(path), ".epub") }

// read finds out what the file at path holds.
func read(path string, f *store.LibraryFile) error {
	sha, err := adding.FileSHA(path)
	if err != nil {
		return err
	}
	f.SHA256 = sha
	b, err := epub.Open(path)
	if err == nil {
		if _, sentences, _ := adding.Prepare(b, nil); len(sentences) == 0 {
			err = errors.New("no readable text")
		}
	}
	if err != nil {
		f.Unreadable = "can't read this file"
		return nil
	}
	f.Title, f.Author, f.Series, f.SeriesIndex = b.Title, b.Author, b.Series, b.SeriesIndex
	return nil
}
