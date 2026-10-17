package affected

import (
	"slices"
	"testing"
)

func TestASliceIsNamedOnceUnderEachNarrowing(t *testing.T) {
	named := []Narrowed{{Kind: NarrowedBy, Narrowing: "by-units", OffAt: 4, Reached: 2, Dropped: []string{"a"}}}
	got := []Narrowed{
		{Kind: NarrowedBy, Narrowing: "by-units", OffAt: 4, Reached: 3, Dropped: []string{"a"}},
		{Kind: NarrowedBy, Narrowing: "fields", OffAt: 2, Reached: 3, Dropped: []string{"a", "c"}},
		{Kind: NarrowedBy, Narrowing: "method-sets", OffAt: 5, Reached: 3, Dropped: []string{"a", "b"}},
	}

	out := unnamed(named, got, 1)

	want := []Narrowed{
		{Kind: NarrowedBy, Narrowing: "fields", OffAt: 2, Reached: 3, Dropped: []string{"a", "c"}},
		{Kind: NarrowedBy, Narrowing: "method-sets", OffAt: 5, Reached: 3, Dropped: []string{"a", "b"}},
	}

	if len(out) != len(want) {
		t.Fatalf("got %+v", out)
	}

	for i := range want {
		if out[i].Narrowing != want[i].Narrowing || out[i].Reached != want[i].Reached || !slices.Equal(out[i].Dropped, want[i].Dropped) {
			t.Errorf("%d: %+v, want %+v", i, out[i], want[i])
		}
	}

	more := unnamed(named, []Narrowed{{Kind: NarrowedBy, Narrowing: "by-units", OffAt: 4, Reached: 3, Dropped: []string{"a", "d"}}}, 1)
	if len(more) != 1 || !slices.Equal(more[0].Dropped, []string{"d"}) || more[0].Reached != 2 {
		t.Errorf("an entry that lost a slice: %+v", more)
	}
}
