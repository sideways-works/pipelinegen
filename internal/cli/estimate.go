package cli

import (
	"path/filepath"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/durations"
)

// estimate gives each suite of the plan that keeps a durations file its recorded test time, for what
// it selected and for every slice, from the durations file in the tree at root.
func estimate(plan *affected.Plan, cfg *config.Config, root string) {
	for i := range plan.Suites {
		sp := &plan.Suites[i]

		s, ok := cfg.Suites[sp.Suite]
		if !ok || s.Durations == "" {
			continue
		}

		file, err := durations.Read(filepath.Join(root, cfg.Bazel.Workspace, s.Durations))
		if err != nil {
			continue
		}

		all := sp.Slices()
		if len(all) == 0 {
			all = sp.Selected
		}

		sp.Estimate = estimateOf(file, all, sp.Selected)
	}
}

// estimateOf is the recorded time of all and of selected, a slice without one at the median of those
// with one; nil when none has one.
func estimateOf(file *durations.File, all, selected []bazel.Slice) *affected.Estimate {
	seconds := map[string]float64{}

	var known []float64

	for _, sl := range all {
		var (
			v        float64
			measured bool
		)

		for _, src := range sl.Sources {
			if s, ok := file.Seconds(src, nil); ok {
				v += s
				measured = true
			}
		}

		if measured {
			seconds[sl.Name] = v
			known = append(known, v)
		}
	}

	if len(known) == 0 {
		return nil
	}

	slices.Sort(known)
	median := known[len(known)/2]

	of := func(sl bazel.Slice) float64 {
		if v, ok := seconds[sl.Name]; ok {
			return v
		}

		return median
	}

	e := &affected.Estimate{Measured: len(known), Slices: len(all)}

	for _, sl := range all {
		e.AllSeconds += of(sl)
	}

	for _, sl := range selected {
		e.SelectedSeconds += of(sl)
	}

	return e
}
