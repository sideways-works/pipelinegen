// Package piperun is pipelinegen's side of running on piperun: the emission a run's plan becomes,
// the feed a session's slices come through, and the item lists piperun confirms.
package piperun

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Slice is one slice of a pool as its sessions run it: what a batch's slices.json says of it, and the
// peak the slice should reach, for a session to admit it by.
type Slice struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Target      string   `json:"target,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Tests       []string `json:"tests,omitempty"`
	PredictedMB int64    `json:"predicted_mb,omitempty"`
	// Seconds is the slice's recorded duration; Measured is false when none is recorded.
	Seconds  float64 `json:"-"`
	Measured bool    `json:"-"`
}

// Suite is one suite of a run's plan, as far as its emission goes: whether it has a prepare, the
// slices still to run and the whole-suite checks the plan selected.
type Suite struct {
	Name    string
	Prepare bool
	Slices  []Slice
	Whole   []string
}

// ErrLimit is an emission piperun would refuse.
var ErrLimit = errors.New("the emission is over piperun's limit")

var errName = errors.New("no instance name for piperun")

// Emit is the emission of a run's plan: per suite its prepare, the pool of its slices longest first,
// its whole-suite checks, and its end, which waits for all of them to end, however they did. A suite
// with nothing to run gets its end alone, so the run's end still finds every suite's.
func Emit(suites []Suite) (plugin.Emission, error) {
	em := plugin.Emission{Jobs: []plugin.EmitJob{}}

	for _, s := range suites {
		job := func(name string, after ...plugin.EmitAfter) {
			em.Jobs = append(em.Jobs, plugin.EmitJob{Template: name, Name: name, Params: map[string]any{}, After: after})
		}

		run := len(s.Slices) > 0 || len(s.Whole) > 0

		var (
			before []plugin.EmitAfter
			ends   []plugin.EmitAfter
		)

		if run && s.Prepare {
			job(plugin.PiperunPrepare(s.Name))

			before = []plugin.EmitAfter{{Job: plugin.PiperunPrepare(s.Name)}}
			ends = append(ends, plugin.EmitAfter{Job: plugin.PiperunPrepare(s.Name), Ended: true})
		}

		// the checks run once every slice passed, as in a suite job whose batches failed
		if len(s.Slices) > 0 {
			pool := plugin.PiperunSlices(s.Name)
			em.Pools = append(em.Pools, plugin.EmitPool{Template: pool, Name: pool, Items: Longest(s.Slices), After: before})

			before = []plugin.EmitAfter{{Job: pool}}
			ends = append(ends, plugin.EmitAfter{Job: pool, Ended: true})
		}

		for _, w := range s.Whole {
			name := plugin.PiperunWhole(s.Name, w)
			job(name, before...)

			ends = append(ends, plugin.EmitAfter{Job: name, Ended: true})
		}

		job(plugin.PiperunEnd(s.Name), ends...)
	}

	if err := checkNames(em); err != nil {
		return plugin.Emission{}, err
	}

	return em, nil
}

// Longest orders the slices by recorded duration, longest first, a slice without one at the median of
// those with one; ties go by name.
func Longest(in []Slice) []string {
	var known []float64

	for _, s := range in {
		if s.Measured {
			known = append(known, s.Seconds)
		}
	}

	median := 1.0

	if len(known) > 0 {
		slices.Sort(known)
		median = known[len(known)/2]
	}

	secs := func(s Slice) float64 {
		if s.Measured {
			return s.Seconds
		}

		return median
	}

	sorted := slices.Clone(in)
	sort.SliceStable(sorted, func(i, j int) bool {
		if a, b := secs(sorted[i]), secs(sorted[j]); a != b {
			return a > b
		}

		return sorted[i].Name < sorted[j].Name
	})

	out := make([]string, 0, len(sorted))
	for _, s := range sorted {
		out = append(out, s.Name)
	}

	return out
}

// piperun takes a name of one path segment, not starting with "_", unique in the build.
func checkNames(em plugin.Emission) error {
	seen := map[string]bool{}

	check := func(name string) error {
		switch {
		case name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, "_") || name == "." || name == "..":
			return fmt.Errorf("%w: %q must be one path segment, not starting with _: rename the suite or whole-suite check", errName, name)
		case seen[name]:
			return fmt.Errorf("%w: %q names two instances: rename a suite or whole-suite check", errName, name)
		}

		seen[name] = true

		return nil
	}

	for _, j := range em.Jobs {
		if err := check(j.Name); err != nil {
			return err
		}
	}

	for _, p := range em.Pools {
		if err := check(p.Name); err != nil {
			return err
		}
	}

	return nil
}

// Encode is the emission as piperun reads it, refused over any of its limits, each by name.
func Encode(em plugin.Emission) ([]byte, error) {
	if n := len(em.Jobs) + len(em.Pools); n > plugin.EmitMaxInstances {
		return nil, fmt.Errorf("%w: %d instances, more than the %d piperun takes in one emission", ErrLimit, n, plugin.EmitMaxInstances)
	}

	for _, p := range em.Pools {
		if len(p.Items) > plugin.EmitMaxPoolItems {
			return nil, fmt.Errorf("%w: pool %s holds %d slices, more than the %d piperun takes in one pool", ErrLimit, p.Name, len(p.Items), plugin.EmitMaxPoolItems)
		}
	}

	data, err := json.Marshal(em)
	if err != nil {
		return nil, fmt.Errorf("emission: %w", err)
	}

	if len(data) > plugin.EmitMaxBytes {
		return nil, fmt.Errorf("%w: %d bytes, more than the %d piperun takes in one emission", ErrLimit, len(data), plugin.EmitMaxBytes)
	}

	return data, nil
}
