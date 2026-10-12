package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/jheck90/gilgamesh/internal/adding"
	"github.com/jheck90/gilgamesh/internal/store"
)

// queued is one entry on the queue page.
type queued struct {
	ID       int64
	Name     string // the book's title once stored, else the file's name
	Started  string // who started it
	State    string
	Reason   string
	Call     string // "call 3 of 77" while reading
	Yours    bool   // the viewer may pause, resume or cancel it
	CanPause bool
}

func (s *Server) queued(r *http.Request, viewer store.User, a store.QueuedAdd) (queued, error) {
	q := queued{ID: a.ID, Name: filepath.Base(a.Path), Started: a.UserName, Reason: a.Reason,
		Yours: a.UserID == viewer.ID || viewer.IsAdmin}
	if a.BookID != 0 {
		b, ok, err := s.Store.BookByID(r.Context(), a.BookID)
		if err != nil {
			return q, err
		}
		if ok {
			q.Name = b.Title
		}
	}
	switch a.State {
	case store.AddWaiting:
		q.State = "Waiting"
	case store.AddAdding:
		q.State = "Adding"
	case store.AddEditing:
		q.State = "Editing"
	case store.AddPaused:
		q.State = "Paused"
	case store.AddDone:
		q.State = "Done"
	}
	if p := a.Progress; p.TotalChunks > 0 && !p.Done() {
		q.Call = fmt.Sprintf("call %d of %d", p.NextChunk+1, p.TotalChunks)
	}
	q.CanPause = a.State == store.AddWaiting || a.State == store.AddAdding || a.State == store.AddEditing
	return q, nil
}

var notYours = httpError{http.StatusForbidden, "Only the reader who started adding this book, or the admin, can change it."}

func (s *Server) addingQueue(w http.ResponseWriter, r *http.Request) {
	user, err := s.user(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	adds, err := s.Store.Adds(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var rows []queued
	for _, a := range adds {
		q, err := s.queued(r, user, a)
		if err != nil {
			s.fail(w, err)
			return
		}
		rows = append(rows, q)
	}
	s.render(w, "adding", map[string]any{"Title": "Books being added", "Adds": rows})
}

// changeAdding runs change on the entry in the path as the reader, then
// shows the queue.
func (s *Server) changeAdding(w http.ResponseWriter, r *http.Request, change func(store.User, int64) error) {
	user, err := s.user(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, notFound)
		return
	}
	if err := change(user, id); errors.Is(err, adding.ErrNotYours) {
		s.fail(w, notYours)
		return
	} else if err != nil {
		s.fail(w, httpError{http.StatusConflict, err.Error()})
		return
	}
	http.Redirect(w, r, "/adding", http.StatusSeeOther)
}

func (s *Server) pauseAdding(w http.ResponseWriter, r *http.Request) {
	s.changeAdding(w, r, func(u store.User, id int64) error { return s.Adding.Pause(r.Context(), u, id) })
}

func (s *Server) resumeAdding(w http.ResponseWriter, r *http.Request) {
	s.changeAdding(w, r, func(u store.User, id int64) error { return s.Adding.Resume(r.Context(), u, id) })
}

// cancelAdding asks first, since it removes the partly added book.
func (s *Server) cancelAdding(w http.ResponseWriter, r *http.Request) {
	if r.PostFormValue("confirm") == "yes" {
		s.changeAdding(w, r, func(u store.User, id int64) error { return s.Adding.Cancel(r.Context(), u, id) })
		return
	}
	user, err := s.user(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, ok, err := s.Store.QueuedAddByID(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		s.fail(w, notFound)
		return
	}
	q, err := s.queued(r, user, a)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !q.Yours {
		s.fail(w, notYours)
		return
	}
	s.render(w, "cancel", map[string]any{"Title": "Cancel adding", "Add": q, "Started": a.BookID != 0 && !a.Progress.Done()})
}
