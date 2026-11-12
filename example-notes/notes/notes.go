// Package notes keeps notes in memory.
package notes

import (
	"errors"
	"sort"
	"sync"
)

// Note is one note.
type Note struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// ErrNotFound is what Get returns for an id it doesn't have.
var ErrNotFound = errors.New("no such note")

// Store holds notes by id.
type Store struct {
	mu    sync.Mutex
	next  int
	notes map[int]Note
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{next: 1, notes: map[int]Note{}}
}

// Add stores a note and returns it with its new id.
func (s *Store) Add(title, body string) Note {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := Note{ID: s.next, Title: title, Body: body}
	s.notes[n.ID] = n
	s.next++

	return n
}

// Get returns the note with the id.
func (s *Store) Get(id int) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, ok := s.notes[id]
	if !ok {
		return Note{}, ErrNotFound
	}

	return n, nil
}

// List returns every note, oldest first.
func (s *Store) List() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Note, 0, len(s.notes))
	for _, n := range s.notes {
		out = append(out, n)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}
