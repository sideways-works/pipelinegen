// Package batch deals a run's selected slices into batches of near-equal time, so that one task
// can set up once and run a whole batch.
package batch

import (
	"math"
	"slices"
	"sort"
)

type Slice struct {
	Name string
	Secs float64
	// Recorded is false for a slice with no recorded time: it counts as the median of the recorded
	// ones (one second when none is).
	Recorded bool
}

// Deal deals the longest slice first into the batch with the least time so far (ties to the lower);
// the same slices in any order give the same batches, ordered by first slice, none empty.
func Deal(in []Slice, n int) [][]string {
	if n < 1 {
		n = 1
	}

	sorted := withMedian(in)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Secs != sorted[j].Secs {
			return sorted[i].Secs > sorted[j].Secs
		}

		return sorted[i].Name < sorted[j].Name
	})

	batches := make([][]string, min(n, len(sorted)))
	load := make([]float64, len(batches))

	for _, s := range sorted {
		best := 0

		for b := range batches {
			if load[b] < load[best] {
				best = b
			}
		}

		batches[best] = append(batches[best], s.Name)
		load[best] += s.Secs
	}

	for _, b := range batches {
		slices.Sort(b)
	}

	sort.Slice(batches, func(i, j int) bool { return batches[i][0] < batches[j][0] })

	return batches
}

// Count is how many batches of about minutes the slices' total time needs, rounded up: at least
// parallel, so a few slices still run side by side; at most upper, if set, and the slices.
func Count(in []Slice, minutes, parallel, upper int) int {
	total := 0.0
	for _, s := range withMedian(in) {
		total += s.Secs
	}

	n := 1
	if minutes > 0 {
		n = int(math.Ceil(total / float64(minutes*60)))
	}

	n = max(n, parallel, 1)

	if upper > 0 {
		n = min(n, upper)
	}

	return min(n, max(len(in), 1))
}

func withMedian(in []Slice) []Slice {
	var known []float64

	for _, s := range in {
		if s.Recorded {
			known = append(known, s.Secs)
		}
	}

	median := 1.0

	if n := len(known); n > 0 {
		sort.Float64s(known)
		median = (known[(n-1)/2] + known[n/2]) / 2
	}

	out := slices.Clone(in)

	for i := range out {
		if !out[i].Recorded {
			out[i].Secs = median
		}
	}

	return out
}
