package search

import (
	"testing"

	"example.com/notes/notes"
)

func TestFind(t *testing.T) {
	all := []notes.Note{
		{ID: 1, Title: "Groceries", Body: "milk, eggs"},
		{ID: 2, Title: "Books", Body: "a novel about eggs"},
	}

	if got := Find(all, "Milk"); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("Find(milk) = %v", got)
	}

	if got := Find(all, "eggs"); len(got) != 2 {
		t.Fatalf("Find(eggs) = %v", got)
	}
}
