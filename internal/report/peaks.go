package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// A suite's recent memory peaks and cores, by slice, in one ref the trusted runs' suite ends keep:
// `batches` predicts each batch's peak and cores from it, and a batch waits for them before it starts.
const (
	peaksFile   = "peaks.json"
	PeaksKept   = 10
	peakRetries = 3
)

func PeaksRef(suite string) string { return refRoot + "peaks/" + suite }

// StepPeaksRef keeps the recent peaks, cores and seconds of the pipeline's own steps, by step: such a step
// waits for the memory and the cores they predict before its heavy part, and holds its cores for about as
// long as it took.
const StepPeaksRef = refRoot + "step-peaks"

type peakSample struct {
	MB     int64  `json:"mb"`
	Commit string `json:"commit"`
	// Cores is the slice's CPU seconds over its seconds: the cores it kept busy on average.
	Cores float64 `json:"cores,omitempty"`
	// Seconds is how long a step of the pipeline's own took from its start; a slice's are its durations'.
	Seconds float64 `json:"seconds,omitempty"`
}

// Peak is what a slice or a step used at one commit: the most memory, in MB, its cores and, for a step,
// its seconds, each 0 when unmeasured.
type Peak struct {
	MB      int64
	Cores   float64
	Seconds float64
}

type peaksDoc struct {
	Slices map[string][]peakSample `json:"slices"`
}

func readPeaks(ctx context.Context, store *gitstore.Store, at string) (peaksDoc, string, error) {
	doc := peaksDoc{Slices: map[string][]peakSample{}}

	ref, ok, err := store.First(ctx, []string{at})
	if err != nil || !ok {
		return doc, "", err
	}

	data, ok, err := store.File(ctx, ref.Commit, peaksFile)
	if err != nil {
		return doc, "", err
	}

	// a file that won't read is written anew
	if ok && json.Unmarshal(data, &doc) != nil || doc.Slices == nil {
		doc.Slices = map[string][]peakSample{}
	}

	return doc, ref.Commit, nil
}

// Peaks reads a suite's recent peaks, oldest first, in MB, by slice.
func Peaks(ctx context.Context, store *gitstore.Store, suite string) (map[string][]int64, error) {
	out, err := peaksAt(ctx, store, PeaksRef(suite))
	if err != nil {
		return nil, fmt.Errorf("peaks of %s: %w", suite, err)
	}

	return out, nil
}

// StepUse is what a step of the pipeline's own used at its recent commits, oldest first: its peaks in MB,
// its cores and its seconds, each only where measured.
type StepUse struct {
	MB      []int64
	Cores   []float64
	Seconds []float64
}

// StepUses reads the recent use of the pipeline's own steps, by step.
func StepUses(ctx context.Context, store *gitstore.Store) (map[string]StepUse, error) {
	doc, _, err := readPeaks(ctx, store, StepPeaksRef)
	if err != nil {
		return nil, fmt.Errorf("the steps' peaks: %w", err)
	}

	out := map[string]StepUse{}

	for name, samples := range doc.Slices {
		var u StepUse

		for _, s := range samples {
			if s.MB > 0 {
				u.MB = append(u.MB, s.MB)
			}

			if s.Cores > 0 {
				u.Cores = append(u.Cores, s.Cores)
			}

			if s.Seconds > 0 {
				u.Seconds = append(u.Seconds, s.Seconds)
			}
		}

		out[name] = u
	}

	return out, nil
}

func peaksAt(ctx context.Context, store *gitstore.Store, at string) (map[string][]int64, error) {
	doc, _, err := readPeaks(ctx, store, at)
	if err != nil {
		return nil, err
	}

	out := map[string][]int64{}

	for name, samples := range doc.Slices {
		for _, s := range samples {
			// a slice's cores may be recorded without its peak
			if s.MB > 0 {
				out[name] = append(out[name], s.MB)
			}
		}
	}

	return out, nil
}

// Cores reads a suite's recent cores, oldest first, by slice: each the CPU seconds a slice used over
// its seconds.
func Cores(ctx context.Context, store *gitstore.Store, suite string) (map[string][]float64, error) {
	doc, _, err := readPeaks(ctx, store, PeaksRef(suite))
	if err != nil {
		return nil, fmt.Errorf("cores of %s: %w", suite, err)
	}

	out := map[string][]float64{}

	for name, samples := range doc.Slices {
		for _, s := range samples {
			if s.Cores > 0 {
				out[name] = append(out[name], s.Cores)
			}
		}
	}

	return out, nil
}

// recordPeaks adds a trusted run's peaks, one per slice and commit, so a suite's end told again adds
// nothing; a proposed change's run never writes them, and a failed write only warns.
func (r *Reporter) recordPeaks(ctx context.Context, run Run, suite string) {
	if r.ID.Proposed || r.Store.Proposed {
		return
	}

	measured := map[string]Peak{}

	for _, s := range run.Suites {
		if s.Name != suite {
			continue
		}

		for _, sl := range s.Slices {
			if sl.PeakMB > 0 || sl.Cores > 0 {
				measured[sl.display()] = Peak{MB: sl.PeakMB, Cores: sl.Cores}
			}
		}
	}

	if len(measured) == 0 {
		return
	}

	if err := AddPeaks(ctx, r.Store, suite, r.ID.Commit, measured); err != nil {
		r.warnf("keeping %s's memory peaks: %v", suite, err)
	}
}

// AddPeaks adds a peak and cores per slice measured at commit to a suite's peaks, the last PeaksKept of
// each; a slice that has one of the commit already keeps it.
func AddPeaks(ctx context.Context, store *gitstore.Store, suite, commit string, measured map[string]Peak) error {
	return addPeaksAt(ctx, store, PeaksRef(suite), "peaks of "+suite, commit, measured)
}

// AddStepPeak adds what a step of the pipeline's own used at commit, its peak, cores and seconds, to the
// steps' peaks, the last PeaksKept of each step; a step that has one of the commit already keeps it.
func AddStepPeak(ctx context.Context, store *gitstore.Store, step, commit string, used Peak) error {
	return addPeaksAt(ctx, store, StepPeaksRef, "peak of "+step, commit, map[string]Peak{step: used})
}

func addPeaksAt(ctx context.Context, store *gitstore.Store, at, what, commit string, measured map[string]Peak) error {
	var err error

	for range peakRetries {
		if err = addPeaks(ctx, store, at, what, commit, measured); !errors.Is(err, gitstore.ErrStale) {
			break
		}
	}

	return err
}

func addPeaks(ctx context.Context, store *gitstore.Store, at, what, full string, measured map[string]Peak) error {
	doc, old, err := readPeaks(ctx, store, at)
	if err != nil {
		return err
	}

	commit, added := short(full), false

	for _, name := range slices.Sorted(maps.Keys(measured)) {
		samples := doc.Slices[name]
		if slices.ContainsFunc(samples, func(s peakSample) bool { return s.Commit == commit }) {
			continue
		}

		m := measured[name]
		samples = append(samples, peakSample{MB: m.MB, Commit: commit, Cores: m.Cores, Seconds: m.Seconds})
		doc.Slices[name], added = samples[max(len(samples)-PeaksKept, 0):], true
	}

	if !added {
		return nil
	}

	data, err := encode(doc)
	if err != nil {
		return err
	}

	_, err = store.Write(ctx, what, gitstore.Update{Ref: at, Old: old, Files: map[string][]byte{peaksFile: data}})

	return err
}
