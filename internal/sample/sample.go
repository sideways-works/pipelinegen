// Package sample picks a sample of a suite's slices: for a change every
// test exercises the same way, a spread of the suite says as much as all of
// it. The pick depends on a seed (the commit), so over many commits every
// slice takes its turn.
package sample

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"sort"
)

// Slice is one slice of the suite with its group and recorded seconds.
type Slice struct {
	Name  string
	Group string
	Secs  float64
}

// Pick returns n slices of the suite, sorted by name: every slice in must,
// even past n; then one slice of every group must doesn't reach, while n
// allows; then the rest shared out by group size. Within a group the slices
// whose hash with the seed comes first are picked, so long slices are
// sampled as often as short ones, and the same slices and seed give the same
// pick.
func Pick(all []Slice, n int, seed string, must []string) []string {
	byName := map[string]Slice{}
	for _, s := range all {
		byName[s.Name] = s
	}

	picked := map[string]bool{}
	count := map[string]int{} // picked slices per group

	for _, m := range must {
		if s, ok := byName[m]; ok && !picked[m] {
			picked[m] = true
			count[s.Group]++
		}
	}

	groups := map[string][]Slice{} // the slices left, per group, in pick order
	size := map[string]int{}

	for _, name := range slices.Sorted(keys(byName)) {
		s := byName[name]
		size[s.Group]++

		if !picked[name] {
			groups[s.Group] = append(groups[s.Group], s)
		}
	}

	for g := range groups {
		order(groups[g], seed)
	}

	left := n - len(picked)

	for _, g := range groupOrder(groups, seed) {
		if left <= 0 {
			break
		}

		if count[g] == 0 {
			take(groups, g, picked, count)

			left--
		}
	}

	share := shares(size, n)

	for ; left > 0; left-- {
		g := neediest(groups, share, count, seed)
		if g == "" {
			break
		}

		take(groups, g, picked, count)
	}

	return slices.Sorted(keys(picked))
}

func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// rank is a name's place for a seed: a hash, so every seed orders the
// slices anew and each is equally likely to come first.
func rank(seed, kind, name string) uint64 {
	h := sha256.Sum256([]byte(seed + "\x00" + kind + "\x00" + name))

	return binary.BigEndian.Uint64(h[:8])
}

func order(ss []Slice, seed string) {
	sort.Slice(ss, func(i, j int) bool {
		ri, rj := rank(seed, "slice", ss[i].Name), rank(seed, "slice", ss[j].Name)
		if ri != rj {
			return ri < rj
		}

		if ss[i].Secs != ss[j].Secs {
			return ss[i].Secs < ss[j].Secs
		}

		return ss[i].Name < ss[j].Name
	})
}

func groupOrder(groups map[string][]Slice, seed string) []string {
	out := slices.Collect(keys(groups))
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank(seed, "group", out[i]), rank(seed, "group", out[j])
		if ri != rj {
			return ri < rj
		}

		return out[i] < out[j]
	})

	return out
}

func take(groups map[string][]Slice, g string, picked map[string]bool, count map[string]int) {
	s := groups[g][0]
	groups[g] = groups[g][1:]
	picked[s.Name] = true
	count[g]++
}

// shares is each group's share of n: a group whose share by size comes to
// less than one slice gets one, and the others share the rest by size.
func shares(size map[string]int, n int) map[string]float64 {
	out := map[string]float64{}
	forced := map[string]bool{}

	for {
		rest := float64(n - len(forced))

		var total float64

		for g, sz := range size {
			if !forced[g] {
				total += float64(sz)
			}
		}

		changed := false

		for _, g := range slices.Sorted(keys(size)) {
			if forced[g] {
				out[g] = 1

				continue
			}

			out[g] = rest * float64(size[g]) / total
			if out[g] < 1 && rest > 0 {
				forced[g] = true
				changed = true
			}
		}

		if !changed {
			return out
		}
	}
}

func neediest(groups map[string][]Slice, share map[string]float64, count map[string]int, seed string) string {
	best := ""

	var bestNeed float64

	for _, g := range groupOrder(groups, seed) {
		if len(groups[g]) == 0 {
			continue
		}

		need := share[g] - float64(count[g])
		if best == "" || need > bestNeed {
			best, bestNeed = g, need
		}
	}

	return best
}
