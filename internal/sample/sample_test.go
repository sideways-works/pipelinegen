package sample

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"
)

// suite is shaped like a browser suite: two big groups, a few middling
// ones and many groups of one; every third slice is ten times as long.
func suite() []Slice {
	var out []Slice

	add := func(group string, n int) {
		for i := range n {
			secs := 10.0
			if i%3 == 0 {
				secs = 100
			}

			out = append(out, Slice{Name: fmt.Sprintf("%s-%02d", group, i), Group: group, Secs: secs})
		}
	}

	add("host", 65)
	add("console", 44)
	add("event", 8)
	add("door", 6)
	add("wallet", 5)

	for i := range 12 {
		add("solo"+strconv.Itoa(i), 1)
	}

	return out
}

func groupsOf(all []Slice, picked []string) map[string]int {
	group := map[string]string{}
	for _, s := range all {
		group[s.Name] = s.Group
	}

	out := map[string]int{}
	for _, p := range picked {
		out[group[p]]++
	}

	return out
}

func TestEveryGroupThenTheirShares(t *testing.T) {
	all := suite()

	for seed := range 200 {
		got := Pick(all, 34, strconv.Itoa(seed), nil)
		if len(got) != 34 {
			t.Fatalf("seed %d: %d picked", seed, len(got))
		}

		counts := groupsOf(all, got)

		// the groups of one take one slot each; the five others share the
		// remaining 22 by size, each within one slice of its share
		for g, share := range map[string]float64{"host": 22 * 65.0 / 128, "console": 22 * 44.0 / 128, "event": 22 * 8.0 / 128} {
			if c := counts[g]; math.Abs(float64(c)-share) > 1.5 {
				t.Fatalf("seed %d: group %s has %d, its share is %.1f: %v", seed, g, c, share, counts)
			}
		}

		if counts["door"] != 1 || counts["wallet"] != 1 {
			t.Fatalf("seed %d: small groups: %v", seed, counts)
		}

		for i := range 12 {
			if counts["solo"+strconv.Itoa(i)] != 1 {
				t.Fatalf("seed %d: a group of one is missing: %v", seed, counts)
			}
		}
	}
}

func TestFewerSlotsThanGroupsSpreadOverGroups(t *testing.T) {
	all := suite()

	for seed := range 50 {
		got := Pick(all, 5, strconv.Itoa(seed), nil)
		if counts := groupsOf(all, got); len(got) != 5 || len(counts) != 5 {
			t.Fatalf("seed %d: %v over %v", seed, got, counts)
		}
	}
}

func TestTheSameSeedPicksTheSame(t *testing.T) {
	all := suite()
	first := Pick(all, 20, "0123456789ab", []string{"door-03"})

	shuffled := slices.Clone(all)
	slices.Reverse(shuffled)

	if again := Pick(shuffled, 20, "0123456789ab", []string{"door-03"}); !slices.Equal(again, first) {
		t.Fatalf("order changed the pick:\n%v\n%v", first, again)
	}

	distinct := map[string]bool{}
	for seed := range 200 {
		distinct[fmt.Sprint(Pick(all, 20, strconv.Itoa(seed), nil))] = true
	}

	if len(distinct) < 190 {
		t.Fatalf("only %d different picks over 200 seeds", len(distinct))
	}
}

func TestOverManySeedsEverySliceIsSampled(t *testing.T) {
	all := suite()
	seen := map[string]int{}

	for seed := range 200 {
		for _, p := range Pick(all, 24, fmt.Sprintf("commit-%d", seed), nil) {
			seen[p]++
		}
	}

	for _, s := range all {
		if seen[s.Name] == 0 {
			t.Errorf("%s never sampled", s.Name)
		}
	}

	var long, short, nLong, nShort float64

	for _, s := range all {
		if s.Group != "host" {
			continue
		}

		if s.Secs > 10 {
			long += float64(seen[s.Name])
			nLong++
		} else {
			short += float64(seen[s.Name])
			nShort++
		}
	}

	if ratio := (long / nLong) / (short / nShort); ratio < 0.7 || ratio > 1.4 {
		t.Errorf("long slices sampled %.2f times as often as short ones", ratio)
	}
}

func TestMustIsAlwaysIn(t *testing.T) {
	all := suite()
	must := []string{"host-01", "host-02", "wallet-04", "not-a-slice"}

	got := Pick(all, 2, "x", must)
	if !slices.Equal(got, []string{"host-01", "host-02", "wallet-04"}) {
		t.Fatalf("must past n: %v", got)
	}

	got = Pick(all, 20, "x", must)
	if len(got) != 20 || !slices.Contains(got, "wallet-04") || !slices.Contains(got, "host-02") {
		t.Fatalf("must within n: %v", got)
	}

	if counts := groupsOf(all, got); counts["wallet"] != 1 {
		t.Fatalf("wallet got %d", counts["wallet"])
	}
}

func TestEdges(t *testing.T) {
	all := suite()

	if got := Pick(all, 0, "x", nil); len(got) != 0 {
		t.Fatalf("n 0: %v", got)
	}

	if got := Pick(all, 1000, "x", nil); len(got) != len(all) {
		t.Fatalf("n past the suite: %d", len(got))
	}

	if got := Pick(nil, 5, "x", nil); len(got) != 0 {
		t.Fatalf("no slices: %v", got)
	}

	// equal hashes can't be forced, but equal everything else orders by
	// duration, then name
	ss := []Slice{{Name: "b", Secs: 1}, {Name: "a", Secs: 2}}
	order(ss, "")

	if ss[0].Name == ss[1].Name {
		t.Fatal("order lost a slice")
	}
}
