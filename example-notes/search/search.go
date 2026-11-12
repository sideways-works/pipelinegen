// Package search finds notes by the words in them.
package search

import (
	"slices"

	"example.com/notes/notes"
	"example.com/notes/text"
)

// Find returns the notes whose title or body holds every word of the query.
func Find(all []notes.Note, query string) []notes.Note {
	want := text.Words(query)

	var out []notes.Note

	for _, n := range all {
		have := text.Words(n.Title + " " + n.Body)
		if containsAll(have, want) {
			out = append(out, n)
		}
	}

	return out
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}

	return true
}
