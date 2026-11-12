package notes

import "testing"

func TestAddAndGet(t *testing.T) {
	s := NewStore()
	n := s.Add("Groceries", "milk, eggs")

	got, err := s.Get(n.ID)
	if err != nil || got.Title != "Groceries" {
		t.Fatalf("Get(%d) = %v, %v", n.ID, got, err)
	}

	if _, err := s.Get(99); err != ErrNotFound {
		t.Fatalf("Get(99) error = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	s := NewStore()
	s.Add("first", "")
	s.Add("second", "")

	if got := s.List(); len(got) != 2 || got[0].Title != "first" {
		t.Fatalf("List() = %v", got)
	}
}
