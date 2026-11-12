// Package api serves the notes over HTTP, as api/notes.yaml describes:
// the routes come from the generated package notesapi, the handlers are
// here.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"example.com/notes/api/notesapi"
	"example.com/notes/notes"
	"example.com/notes/search"
	"example.com/notes/text"
)

// Handler returns the API's routes over a store.
func Handler(s *notes.Store) http.Handler {
	return notesapi.Handler(server{store: s})
}

// server implements the generated notesapi.ServerInterface.
type server struct {
	store *notes.Store
}

// CreateNote serves POST /notes.
func (s server) CreateNote(w http.ResponseWriter, r *http.Request) {
	var in notesapi.NewNote
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "the body is not JSON", http.StatusBadRequest)
		return
	}

	if err := validate(in); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	body := ""
	if in.Body != nil {
		body = *in.Body
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, s.store.Add(text.Title(in.Title), body))
}

func validate(in notesapi.NewNote) error {
	if text.Title(in.Title) == "" {
		return errors.New("a note needs a title")
	}

	return nil
}

// ListNotes serves GET /notes, with an optional query.
func (s server) ListNotes(w http.ResponseWriter, _ *http.Request, params notesapi.ListNotesParams) {
	all := s.store.List()
	if params.Q != nil && *params.Q != "" {
		all = search.Find(all, *params.Q)
	}

	writeJSON(w, all)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
