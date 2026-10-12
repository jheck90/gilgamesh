// Command gmesh is the Gilgamesh command line: add books and look things up
// without spoilers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/jheck90/gilgamesh/internal/secrets"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

const usage = `gmesh: a spoiler-safe companion for the book you're reading.

Usage: gmesh [-db FILE] [-user NAME] <command> [args]

Books
  ingest FILE.epub         add a book and take notes on it (resumes if interrupted);
                           try --dry-run first, then --skip front/back matter
  edit BOOK                have a model tidy a finished book's notes: drop trivia,
                           merge duplicates (try --dry-run --save FILE, then --from FILE)
  books                    list books
  chapters BOOK            list a book's chapters and their numbers
  rm BOOK --yes            remove a book and everything noted from it

Reading (only adding books ever calls a model)
  serve [--addr :8080]     the reading web app (installable on a phone); also adds
                           books queued from it in the background, one at a time
  pos BOOK [--chapter N]   show or set where you are
  entities BOOK            everyone and everything you've met so far
  card BOOK NAME           what you know about NAME so far
  timeline BOOK            in-world dates and days you've reached

Keys
  keygen                   make a GMESH_SECRET for storing keys
  key set|rm|status        your own API key
  key request              ask to use the admin's key
  key list|approve|deny|revoke USER   (admin)

BOOK is a book id or part of its title. Reading commands take --chapter N to
look as of the end of chapter N instead of your saved position.

Environment: GMESH_DB, GMESH_USER, GMESH_SECRET, ANTHROPIC_API_KEY,
GMESH_PROVIDER, GMESH_API_KEY, GMESH_BASE_URL, GMESH_MODEL, GMESH_ADDR,
GMESH_TRUST_HEADER, GMESH_PRICES.
`

type app struct {
	dbPath   string
	userName string
	out      io.Writer
	st       *store.Store
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gmesh:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("gmesh", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(out, usage) }
	a := &app{out: out}
	fs.StringVar(&a.dbPath, "db", envOr("GMESH_DB", "gilgamesh.db"), "database file")
	fs.StringVar(&a.userName, "user", envOr("GMESH_USER", "default"), "reader name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return nil
	}
	defer a.close()
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	switch cmd {
	case "ingest":
		return a.ingest(ctx, rest)
	case "edit":
		return a.edit(ctx, rest)
	case "books":
		return a.books(ctx)
	case "chapters":
		return a.chapterList(ctx, rest)
	case "serve":
		return a.serve(ctx, rest)
	case "rm":
		return a.rm(ctx, rest)
	case "pos":
		return a.pos(ctx, rest)
	case "entities":
		return a.entities(ctx, rest)
	case "card":
		return a.card(ctx, rest)
	case "timeline":
		return a.timeline(ctx, rest)
	case "keygen":
		fmt.Fprintln(out, secrets.GenerateKey())
		return nil
	case "key":
		return a.key(ctx, rest)
	case "help", "-h", "--help":
		fs.Usage()
		return nil
	}
	return fmt.Errorf("unknown command %q (try `gmesh help`)", cmd)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (a *app) store() (*store.Store, error) {
	if a.st == nil {
		st, err := store.Open(a.dbPath)
		if err != nil {
			return nil, err
		}
		a.st = st
	}
	return a.st, nil
}

func (a *app) close() {
	if a.st != nil {
		a.st.Close()
	}
}

func (a *app) user(ctx context.Context) (store.User, error) {
	st, err := a.store()
	if err != nil {
		return store.User{}, err
	}
	return st.EnsureUser(ctx, a.userName)
}

// box opens the instance secret, or returns nil if none is set.
func box() (*secrets.Box, error) {
	s := os.Getenv("GMESH_SECRET")
	if s == "" {
		return nil, nil
	}
	return secrets.NewBox(s)
}

// book finds a book by id or by part of its title.
func (a *app) book(ctx context.Context, ref string) (store.Book, error) {
	st, err := a.store()
	if err != nil {
		return store.Book{}, err
	}
	books, err := st.Books(ctx)
	if err != nil {
		return store.Book{}, err
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for _, b := range books {
			if b.ID == id {
				return b, nil
			}
		}
	}
	var matches []store.Book
	for _, b := range books {
		if strings.Contains(text.Fold(b.Title), text.Fold(ref)) {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return store.Book{}, fmt.Errorf("no book matches %q (see `gmesh books`)", ref)
	}
	var names []string
	for _, b := range matches {
		names = append(names, fmt.Sprintf("%d: %s", b.ID, b.Title))
	}
	return store.Book{}, fmt.Errorf("%q matches several books, use the id:\n  %s", ref, strings.Join(names, "\n  "))
}

// parse handles flags mixed in with positional arguments, which the flag
// package alone stops parsing at.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
