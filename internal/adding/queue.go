package adding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/jheck90/gilgamesh/internal/edit"
	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/extract"
	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/secrets"
	"github.com/jheck90/gilgamesh/internal/store"
)

// Queue adds books in the background, one at a time across the instance,
// so runs in a series stay in reading order and one reader's rate limits
// aren't shared between parallel runs. Each entry runs on the key of the
// reader who started it, checked again before every model call. What's
// done survives a restart: Run picks up at the next call.
type Queue struct {
	Store *store.Store
	Box   *secrets.Box // opens stored keys; nil if GMESH_SECRET isn't set
	Env   keys.Env     // the instance key, which is the admin's
	// NewClient opens a model client; nil means llm.New.
	NewClient func(llm.Config) (llm.Client, error)
	// DBPath is where the database is, to back it up before an edit as
	// `gmesh edit` does. Empty means no backup.
	DBPath string
	Log    io.Writer

	mu   sync.Mutex
	wake chan struct{}
	// The entry being added, if any, and how to stop it. interrupted is
	// set when Pause or Cancel stopped it, so Run leaves its state alone.
	running     int64
	stopRun     context.CancelFunc
	runDone     chan struct{}
	interrupted bool
}

// ErrNotYours is returned when someone other than the reader who started
// an entry, or the admin, tries to pause, resume or cancel it.
var ErrNotYours = errors.New("only the reader who started it, or the admin, can do that")

func (q *Queue) resolve(ctx context.Context, u store.User) (keys.Resolved, error) {
	return keys.Resolve(ctx, q.Store, q.Box, u, q.Env)
}

func (q *Queue) signal() chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	return q.wake
}

func (q *Queue) poke() {
	select {
	case q.signal() <- struct{}{}:
	default:
	}
}

// Start puts a book in the queue for user, who must have a key that works
// now. a.UserID is set from user.
func (q *Queue) Start(ctx context.Context, user store.User, a store.QueuedAdd) (int64, error) {
	if _, err := q.resolve(ctx, user); err != nil {
		return 0, err
	}
	if _, ok, err := q.Store.PendingAddBySHA(ctx, a.SHA256); err != nil {
		return 0, err
	} else if ok {
		return 0, errors.New("that book is already in the queue")
	}
	if b, ok, err := q.Store.BookBySHA(ctx, a.SHA256); err != nil {
		return 0, err
	} else if ok {
		if p, err := q.Store.Progress(ctx, b.ID); err != nil {
			return 0, err
		} else if p.Done() {
			return 0, fmt.Errorf("%s is already added", b.Title)
		}
	}
	a.UserID = user.ID
	id, err := q.Store.QueueAdd(ctx, a)
	if err != nil {
		return 0, err
	}
	q.poke()
	return id, nil
}

// Run adds queued books one at a time until ctx is done. An entry that was
// running when the server last stopped goes first.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.Store.ResumeInterruptedAdds(ctx); err != nil {
		return err
	}
	wake := q.signal()
	for {
		a, runCtx, ok, err := q.claim(ctx)
		if err != nil {
			return err
		}
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
				continue
			}
		}
		err = q.add(runCtx, a)
		if err := q.finish(ctx, a, err); err != nil {
			return err
		}
	}
}

// claim takes the oldest waiting entry, if any, and marks it running. Its
// context is cancelled when it's paused or cancelled.
func (q *Queue) claim(ctx context.Context) (store.QueuedAdd, context.Context, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	a, ok, err := q.Store.NextAdd(ctx)
	if err != nil || !ok {
		return a, nil, false, err
	}
	if err := q.Store.SetAddState(ctx, a.ID, store.AddAdding, ""); err != nil {
		return a, nil, false, err
	}
	q.running, q.runDone, q.interrupted = a.ID, make(chan struct{}), false
	runCtx, stop := context.WithCancel(ctx)
	q.stopRun = stop
	return a, runCtx, true, nil
}

// finish records how the running entry ended.
func (q *Queue) finish(ctx context.Context, a store.QueuedAdd, err error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopRun()
	defer close(q.runDone)
	q.running = 0
	if ctx.Err() != nil {
		// Shutting down: leave it running, to resume on restart.
		return ctx.Err()
	}
	if q.interrupted {
		return nil
	}
	state, reason := store.AddDone, ""
	if err != nil {
		state, reason = store.AddPaused, err.Error()
		q.logf("gmesh: adding %s paused: %v\n", filepath.Base(a.Path), err)
	}
	return q.Store.SetAddState(ctx, a.ID, state, reason)
}

// mine loads entry id for someone about to change it.
func (q *Queue) mine(ctx context.Context, by store.User, id int64) (store.QueuedAdd, error) {
	a, ok, err := q.Store.QueuedAddByID(ctx, id)
	if err != nil {
		return a, err
	}
	if !ok {
		return a, errors.New("no such book in the queue")
	}
	if a.UserID != by.ID && !by.IsAdmin {
		return a, ErrNotYours
	}
	return a, nil
}

// interrupt stops entry id if it's the one running, returning a channel
// closed once it has stopped, or nil. q.mu must be held.
func (q *Queue) interrupt(id int64) chan struct{} {
	if q.running != id {
		return nil
	}
	q.interrupted = true
	q.stopRun()
	return q.runDone
}

// Pause stops an entry, keeping what's done so far, and lets the next one
// start.
func (q *Queue) Pause(ctx context.Context, by store.User, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	a, err := q.mine(ctx, by, id)
	if err != nil {
		return err
	}
	if a.State == store.AddPaused || a.State == store.AddDone {
		return fmt.Errorf("it's already %s", a.State)
	}
	if err := q.Store.SetAddState(ctx, id, store.AddPaused, "paused by "+by.Name); err != nil {
		return err
	}
	q.interrupt(id)
	return nil
}

// Resume puts a paused entry back in line.
func (q *Queue) Resume(ctx context.Context, by store.User, id int64) error {
	defer q.poke() // after unlocking
	q.mu.Lock()
	defer q.mu.Unlock()
	a, err := q.mine(ctx, by, id)
	if err != nil {
		return err
	}
	if a.State != store.AddPaused {
		return fmt.Errorf("it isn't paused, it's %s", a.State)
	}
	return q.Store.SetAddState(ctx, id, store.AddWaiting, "")
}

// Cancel takes an entry out of the queue and removes the partly added
// book, as `gmesh rm` would. A book that's fully read stays; only the edit
// still to come is cancelled.
func (q *Queue) Cancel(ctx context.Context, by store.User, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		a, err := q.mine(ctx, by, id)
		if err != nil {
			return err
		}
		if a.State == store.AddDone {
			return errors.New("it's already added; remove the book itself instead")
		}
		if stopped := q.interrupt(id); stopped != nil {
			q.mu.Unlock()
			<-stopped
			q.mu.Lock()
			continue
		}
		if a.BookID != 0 && !a.Progress.Done() {
			if err := q.Store.DeleteBook(ctx, a.BookID); err != nil {
				return err
			}
		}
		return q.Store.DeleteAdd(ctx, id)
	}
}

// add reads the book in entry a, storing it first if it isn't yet.
func (q *Queue) add(ctx context.Context, a store.QueuedAdd) error {
	user, ok, err := q.Store.UserByID(ctx, a.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the reader who started this is gone")
	}
	res, err := q.resolve(ctx, user)
	if err != nil {
		return err
	}
	book, err := q.stored(ctx, a)
	if err != nil {
		return err
	}
	cfg := RunConfig(res.Config, a.Model, llm.DefaultIngestModel, a.Effort)
	client, err := q.client(cfg)
	if err != nil {
		return err
	}
	x := &extract.Extractor{Store: q.Store, LLM: client, UserID: user.ID, KeyOwnerID: res.OwnerID, Effort: cfg.Effort, Log: q.Log}
	// Recheck before every call: a loan may be revoked, or its allowance
	// run out, partway through a book.
	x.Allow = func(ctx context.Context) error {
		_, err := q.resolve(ctx, user)
		return err
	}
	if err := x.Run(ctx, book); err != nil || !a.ThenEdit {
		return err
	}
	if _, edited, err := q.Store.LastEdit(ctx, book.ID); err != nil || edited {
		return err
	}
	if err := q.setState(ctx, a.ID, store.AddEditing); err != nil {
		return err
	}
	return q.edit(ctx, user, book)
}

// setState moves the running entry on, unless it was just paused.
func (q *Queue) setState(ctx context.Context, id int64, state string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.interrupted {
		return context.Canceled
	}
	return q.Store.SetAddState(ctx, id, state, "")
}

// edit tidies a finished book's notes, as `gmesh edit` does, on the same
// key and with no second confirmation.
func (q *Queue) edit(ctx context.Context, user store.User, book store.Book) error {
	res, err := q.resolve(ctx, user)
	if err != nil {
		return err
	}
	cfg := RunConfig(res.Config, "", llm.DefaultAnthropicModel, "medium")
	client, err := q.client(cfg)
	if err != nil {
		return err
	}
	notes, err := q.Store.BookNotes(ctx, book)
	if err != nil {
		return err
	}
	raw, used, err := (&edit.Editor{LLM: client}).Ask(ctx, notes)
	usage := &store.UsageRec{UserID: user.ID, KeyOwnerID: res.OwnerID, Provider: client.Provider(), Model: client.Model(),
		Effort: cfg.Effort, InputTokens: used.InputTokens, OutputTokens: used.OutputTokens}
	plan := edit.Plan{Edit: store.Edit{BookID: book.ID, SeriesID: book.SeriesID}}
	if err == nil {
		plan, err = edit.Check(notes, raw)
	}
	if err == nil && q.DBPath != "" {
		_, err = Backup(ctx, q.Store, q.DBPath, book.ID)
	}
	if err != nil {
		// Still count the tokens, so caps stay honest.
		q.Store.ApplyEdit(context.WithoutCancel(ctx), store.Edit{BookID: book.ID, SeriesID: book.SeriesID, Usage: usage})
		return err
	}
	plan.Edit.Usage, plan.Edit.Model = usage, client.Model()
	_, err = q.Store.ApplyEdit(ctx, plan.Edit)
	return err
}

// stored returns the book entry a adds, storing it from the file the first
// time. The file must still be the one confirmed.
func (q *Queue) stored(ctx context.Context, a store.QueuedAdd) (store.Book, error) {
	if a.BookID != 0 {
		b, ok, err := q.Store.BookByID(ctx, a.BookID)
		if err == nil && !ok {
			err = errors.New("the book was removed")
		}
		return b, err
	}
	if b, ok, err := q.Store.BookBySHA(ctx, a.SHA256); err != nil || ok {
		if ok {
			err = q.Store.SetAddBook(ctx, a.ID, b.ID)
		}
		return b, err
	}
	sha, err := FileSHA(a.Path)
	if err != nil {
		return store.Book{}, err
	}
	if sha != a.SHA256 {
		return store.Book{}, errors.New("the file has changed since adding it was confirmed")
	}
	eb, err := epub.Open(a.Path)
	if err != nil {
		return store.Book{}, err
	}
	skip, err := ParseSkip(a.Skip)
	if err != nil {
		return store.Book{}, err
	}
	chapters, sentences, _ := Prepare(eb, skip)
	if len(sentences) == 0 {
		return store.Book{}, errors.New("no readable text found; DRM-protected books aren't supported")
	}
	sid, err := q.Store.EnsureSeries(ctx, a.Series)
	if err != nil {
		return store.Book{}, err
	}
	later, err := q.Store.LaterInSeries(ctx, sid, a.SeriesIndex)
	if err != nil {
		return store.Book{}, err
	}
	if len(later) > 0 {
		return store.Book{}, fmt.Errorf("%q already has a later book (%s, #%g); add books in reading order", a.Series, later[0].Title, later[0].SeriesIndex)
	}
	id, err := q.Store.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: a.SeriesIndex, Title: eb.Title, Author: eb.Author, SourcePath: a.Path, SHA256: a.SHA256},
		chapters, sentences, extract.CountChunks(chapters, sentences, a.ChunkWords), a.ChunkWords)
	if err != nil {
		return store.Book{}, err
	}
	if err := q.Store.SetAddBook(ctx, a.ID, id); err != nil {
		return store.Book{}, err
	}
	b, _, err := q.Store.BookByID(ctx, id)
	return b, err
}

func (q *Queue) client(c llm.Config) (llm.Client, error) {
	if q.NewClient != nil {
		return q.NewClient(c)
	}
	return llm.New(c)
}

func (q *Queue) logf(format string, args ...any) {
	if q.Log != nil {
		fmt.Fprintf(q.Log, format, args...)
	}
}
