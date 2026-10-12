package adding

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

// fakeModel answers every passage with no notes, and an edit with no
// changes. Before answering call n (from 1) it runs hook, if set.
type fakeModel struct {
	mu      sync.Mutex
	prompts []string
	hook    func(ctx context.Context, n int) error
}

func (m *fakeModel) Provider() string { return llm.ProviderOpenAI }
func (m *fakeModel) Model() string    { return "fake" }

func (m *fakeModel) JSON(ctx context.Context, req llm.Request) ([]byte, llm.Usage, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, req.User)
	n, hook := len(m.prompts), m.hook
	m.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, n); err != nil {
			return nil, llm.Usage{}, err
		}
	}
	if strings.Contains(req.User, "ENTITIES (in order of first appearance") {
		return []byte(`{"category_map": [], "merges": [], "drop_entities": [], "drop_aliases": [], "drop_facts": [], "drop_markers": []}`),
			llm.Usage{InputTokens: 70, OutputTokens: 7}, nil
	}
	return []byte(`{"entities": [], "time_markers": []}`), llm.Usage{InputTokens: 50, OutputTokens: 5}, nil
}

func (m *fakeModel) setHook(h func(ctx context.Context, n int) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hook = h
}

func (m *fakeModel) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

type fixture struct {
	st    *store.Store
	model *fakeModel
	admin store.User
	alice store.User
	dir   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	f := &fixture{st: st, model: &fakeModel{}, dir: dir}
	f.admin, _ = st.EnsureUser(ctx, "admin")
	f.alice, _ = st.EnsureUser(ctx, "alice")
	return f
}

// queue opens a queue on the store, as a fresh start of the server would.
func (f *fixture) queue() *Queue {
	return &Queue{
		Store: f.st,
		Env: keys.Env(func(k string) string {
			return map[string]string{"GMESH_PROVIDER": "openai", "GMESH_BASE_URL": "http://fake", "GMESH_MODEL": "fake"}[k]
		}),
		NewClient: func(llm.Config) (llm.Client, error) { return f.model, nil },
	}
}

// run runs q until the test ends, or until the returned stop is called.
func run(t *testing.T, q *Queue) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := q.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Run: %v", err)
		}
	}()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

// book writes an EPUB with the given number of chapters, one model call
// each, and asks for it to be added.
func (f *fixture) book(t *testing.T, title string, chapters int) store.QueuedAdd {
	t.Helper()
	path := filepath.Join(f.dir, title+".epub")
	var chs []epubtest.Chapter
	for i := 1; i <= chapters; i++ {
		chs = append(chs, epubtest.Chapter{Title: fmt.Sprintf("Chapter %d", i), Paras: []string{
			fmt.Sprintf("In chapter %d of %s the crawlers walked a long way down the stairs and argued about where the boots went.", i, title)}})
	}
	if err := epubtest.Write(path, epubtest.Book{Title: title, Author: "Someone", Chapters: chs}); err != nil {
		t.Fatal(err)
	}
	sha, err := FileSHA(path)
	if err != nil {
		t.Fatal(err)
	}
	return store.QueuedAdd{Path: path, SHA256: sha, Series: title, SeriesIndex: 1, ChunkWords: 1000}
}

func waitFor(t *testing.T, q *Queue, id int64, state string) store.QueuedAdd {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a, ok, err := q.Store.QueuedAddByID(context.Background(), id)
		if err != nil || !ok {
			t.Fatalf("entry %d: %v, %v", id, ok, err)
		}
		if a.State == state {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry %d is %s (%s), want %s", id, a.State, a.Reason, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAddsInTheBackground(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	run(t, q)

	id, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 2)); err == nil {
		t.Error("the same book was queued twice")
	}
	a := waitFor(t, q, id, store.AddDone)
	if a.BookID == 0 || !a.Progress.Done() || a.Progress.TotalChunks != 2 {
		t.Errorf("done entry = %+v", a)
	}
	if n := len(f.model.calls()); n != 2 {
		t.Errorf("%d model calls, want 2", n)
	}
	used, err := f.st.IngestUsage(ctx, "fake")
	if err != nil || len(used) != 1 || used[0].Input != 100 || used[0].Output != 10 {
		t.Errorf("usage = %+v, %v; want 100 in and 10 out on the one book", used, err)
	}
	if _, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 2)); err == nil {
		t.Error("a book already added was queued again")
	}
	if strings.TrimSpace(a.UserName) != "admin" {
		t.Errorf("started by %q, want admin", a.UserName)
	}
}

func TestResumesAfterARestart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	stop := run(t, q)
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 2 {
			go stop() // the server stops partway through the second call
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	id, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 3))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, q, id, store.AddAdding)
	for len(f.model.calls()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if a, _, _ := f.st.QueuedAddByID(ctx, id); a.State != store.AddAdding || a.Progress.NextChunk != 1 {
		t.Fatalf("after stopping: %+v, want still adding at call 2", a)
	}

	f.model.setHook(nil)
	q = f.queue()
	run(t, q)
	waitFor(t, q, id, store.AddDone)
	calls := f.model.calls()
	if len(calls) != 4 || calls[2] != calls[1] {
		t.Errorf("%d calls; want the second asked again after the restart, then the third", len(calls))
	}
}

func TestOneAtATime(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	release := make(chan struct{})
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 1 {
			<-release
		}
		return nil
	})
	run(t, q)
	first, err := q.Start(ctx, f.admin, f.book(t, "First", 2))
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Start(ctx, f.admin, f.book(t, "Second", 2))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, q, first, store.AddAdding)
	time.Sleep(20 * time.Millisecond)
	if a, _, _ := f.st.QueuedAddByID(ctx, second); a.State != store.AddWaiting {
		t.Errorf("second is %s while the first is adding, want waiting", a.State)
	}
	close(release)
	waitFor(t, q, second, store.AddDone)
	calls := f.model.calls()
	if len(calls) != 4 || !strings.Contains(calls[1], "First") || !strings.Contains(calls[2], "Second") {
		t.Errorf("calls weren't one book after the other: %d calls", len(calls))
	}
}

func TestRevokedBorrowingPauses(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	f.st.RequestGrant(ctx, f.alice.ID, "")
	f.st.DecideGrant(ctx, f.alice.ID, store.GrantApproved, 0, f.admin.ID)
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 1 {
			return f.st.DecideGrant(ctx, f.alice.ID, store.GrantRevoked, 0, f.admin.ID)
		}
		return nil
	})
	borrowed, err := q.Start(ctx, f.alice, f.book(t, "Borrowed", 3))
	if err != nil {
		t.Fatal(err)
	}
	own, err := q.Start(ctx, f.admin, f.book(t, "Own", 1))
	if err != nil {
		t.Fatal(err)
	}
	run(t, q)
	a := waitFor(t, q, borrowed, store.AddPaused)
	if !strings.Contains(a.Reason, "revoked") || a.Progress.NextChunk != 1 {
		t.Errorf("paused entry = %+v; want the reason, and the first call kept", a)
	}
	waitFor(t, q, own, store.AddDone)
	if _, err := q.Start(ctx, f.alice, f.book(t, "Another", 1)); err == nil {
		t.Error("alice started a book after her borrowing was revoked")
	}
}

func TestPauseAndResume(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 2 {
			<-ctx.Done() // still thinking when it's paused
			return ctx.Err()
		}
		return nil
	})
	run(t, q)
	id, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 3))
	if err != nil {
		t.Fatal(err)
	}
	for len(f.model.calls()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if err := q.Pause(ctx, f.alice, id); err == nil {
		t.Error("alice paused a book the admin started")
	}
	if err := q.Pause(ctx, f.admin, id); err != nil {
		t.Fatal(err)
	}
	a := waitFor(t, q, id, store.AddPaused)
	if a.Progress.NextChunk != 1 || a.Reason == "" {
		t.Errorf("paused entry = %+v; want the first call kept and a reason", a)
	}
	f.model.setHook(nil)
	if err := q.Resume(ctx, f.admin, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, q, id, store.AddDone)
	if n := len(f.model.calls()); n != 4 {
		t.Errorf("%d calls, want the paused one asked again and the third", n)
	}
}

func TestCancelRemovesThePartlyAddedBook(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	f.st.RequestGrant(ctx, f.alice.ID, "")
	f.st.DecideGrant(ctx, f.alice.ID, store.GrantApproved, 0, f.admin.ID)
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 2 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	run(t, q)
	id, err := q.Start(ctx, f.alice, f.book(t, "Crawl", 3))
	if err != nil {
		t.Fatal(err)
	}
	for len(f.model.calls()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	// The admin may cancel what anyone started, even while it's running.
	if err := q.Cancel(ctx, f.admin, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.st.QueuedAddByID(ctx, id); ok {
		t.Error("the entry is still queued")
	}
	if books, _ := f.st.Books(ctx); len(books) != 0 {
		t.Errorf("books after cancelling = %+v, want none", books)
	}
}

func TestEditsRightAfter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	var id int64
	stateAtEdit := make(chan string, 1)
	f.model.setHook(func(ctx context.Context, n int) error {
		if n == 3 {
			a, _, _ := f.st.QueuedAddByID(ctx, id)
			stateAtEdit <- a.State
		}
		return nil
	})
	add := f.book(t, "Crawl", 2)
	add.ThenEdit = true
	id, err := q.Start(ctx, f.admin, add)
	if err != nil {
		t.Fatal(err)
	}
	run(t, q)
	a := waitFor(t, q, id, store.AddDone)
	select {
	case got := <-stateAtEdit:
		if got != store.AddEditing {
			t.Errorf("state during the edit = %s, want editing", got)
		}
	default:
		t.Fatal("no edit after adding")
	}
	if _, edited, err := f.st.LastEdit(ctx, a.BookID); err != nil || !edited {
		t.Errorf("book not edited: %v", err)
	}
	if used, err := f.st.EditUsage(ctx, "fake"); err != nil || len(used) != 1 || used[0].Input != 70 {
		t.Errorf("edit usage = %+v, %v", used, err)
	}
}

func TestChangedFileIsNotRead(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	add := f.book(t, "Crawl", 2)
	f.book(t, "Crawl", 3) // the file is replaced after confirming
	id, err := q.Start(ctx, f.admin, add)
	if err != nil {
		t.Fatal(err)
	}
	run(t, q)
	a := waitFor(t, q, id, store.AddPaused)
	if !strings.Contains(a.Reason, "changed") || a.BookID != 0 || len(f.model.calls()) != 0 {
		t.Errorf("entry = %+v after %d calls; want paused before storing or reading anything", a, len(f.model.calls()))
	}
}

func TestPausingKeepsACallThatFinished(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	q := f.queue()
	var id int64
	f.model.setHook(func(_ context.Context, n int) error {
		if n == 2 {
			return q.Pause(ctx, f.admin, id) // paused while the reply comes back
		}
		return nil
	})
	id, err := q.Start(ctx, f.admin, f.book(t, "Crawl", 3))
	if err != nil {
		t.Fatal(err)
	}
	run(t, q)
	a := waitFor(t, q, id, store.AddPaused)
	for i := 0; i < 100 && a.Progress.NextChunk < 2; i++ {
		time.Sleep(5 * time.Millisecond)
		a, _, _ = f.st.QueuedAddByID(ctx, id)
	}
	if a.Progress.NextChunk != 2 {
		t.Errorf("next call = %d, want the second call's notes kept", a.Progress.NextChunk+1)
	}
	if used, _ := f.st.IngestUsage(ctx, "fake"); len(used) != 1 || used[0].Input != 100 {
		t.Errorf("usage = %+v, want both calls counted", used)
	}
}
