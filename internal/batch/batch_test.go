package batch

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func rec(name string, secs float64) Slice { return Slice{Name: name, Secs: secs, Recorded: true} }

func TestDealBalancesAndIsDeterministic(t *testing.T) {
	in := []Slice{rec("a", 10), rec("b", 9), rec("c", 8), rec("d", 3), rec("e", 3), rec("f", 1)}

	got := Deal(in, 2)
	want := [][]string{{"a", "d", "e", "f"}, {"b", "c"}} // 17 s each

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	shuffled := slices.Clone(in)
	slices.Reverse(shuffled)

	if again := Deal(shuffled, 2); !reflect.DeepEqual(again, got) {
		t.Fatalf("order changed the batches: %v", again)
	}
}

func TestAnUnrecordedSliceCountsAsTheMedian(t *testing.T) {
	// recorded 2, 4, 10, 30: the median is 7, so the new slice weighs more
	// than b and goes before it
	in := []Slice{rec("a", 30), rec("b", 4), rec("c", 10), rec("d", 2), {Name: "new"}}

	got := Deal(in, 2)
	want := [][]string{{"a"}, {"b", "c", "d", "new"}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if got := Deal([]Slice{{Name: "x"}, {Name: "y"}, {Name: "z"}}, 3); len(got) != 3 {
		t.Fatalf("nothing recorded, equal weights: %v", got)
	}
}

func TestDealEdges(t *testing.T) {
	if got := Deal(nil, 3); len(got) != 0 {
		t.Fatalf("nothing: %v", got)
	}

	if got := Deal([]Slice{rec("a", 1), rec("b", 1)}, 5); !reflect.DeepEqual(got, [][]string{{"a"}, {"b"}}) {
		t.Fatalf("more batches than slices: %v", got)
	}

	if got := Deal([]Slice{rec("a", 1), rec("b", 1)}, 0); !reflect.DeepEqual(got, [][]string{{"a", "b"}}) {
		t.Fatalf("no count is one batch: %v", got)
	}

	// equal times: ties are broken by name, not by input order
	if got := Deal([]Slice{rec("b", 2), rec("a", 2), rec("c", 2)}, 2); !reflect.DeepEqual(got, [][]string{{"a", "c"}, {"b"}}) {
		t.Fatalf("ties: %v", got)
	}
}

func TestCountByMinutes(t *testing.T) {
	two := func(n int) []Slice {
		out := make([]Slice, n)
		for i := range out {
			out[i] = Slice{Name: fmt.Sprintf("s%03d", i), Secs: 120, Recorded: true}
		}

		return out
	}

	// 585 minutes of 2-minute slices plus a 3-minute one
	everything := append(two(291), Slice{Name: "long", Secs: 180, Recorded: true})

	for _, c := range []struct {
		why                      string
		in                       []Slice
		minutes, parallel, upper int
		want                     int
	}{
		{"a change of five slices runs them side by side", two(5), 18, 2, 0, 2},
		{"a run of everything gets small batches", everything, 18, 2, 0, 33},
		{"batches bounds it", everything, 18, 2, 12, 12},
		{"never more than the slices", two(3), 1, 2, 0, 3},
		{"one slice, one batch", two(1), 18, 4, 0, 1},
		{"unrecorded slices count at the median", append(two(9), Slice{Name: "new"}), 6, 1, 0, 4},
	} {
		if got := Count(c.in, c.minutes, c.parallel, c.upper); got != c.want {
			t.Errorf("%s: %d batches, want %d", c.why, got, c.want)
		}
	}
}
