package text

import (
	"slices"
	"testing"
)

func TestWords(t *testing.T) {
	if got := Words("Milk, eggs & 2 Apples!"); !slices.Equal(got, []string{"milk", "eggs", "2", "apples"}) {
		t.Fatalf("Words = %q", got)
	}
}

func TestTitle(t *testing.T) {
	if got := Title("  Groceries "); got != "Groceries" {
		t.Fatalf("Title = %q", got)
	}
}
