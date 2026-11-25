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

// A suite's recent memory peaks, by slice, in one ref the trusted runs' suite ends keep: `batches`
// predicts each batch's peak from it, and a batch waits for that much memory before it starts.
const (
	peaksFile   = "peaks.json"
	PeaksKept   = 10
	peakRetries = 3
)

func PeaksRef(suite string) string { return refRoot + "peaks/" + suite }

type peakSample struct {
	MB     int64  `json:"mb"`
	Commit string `json:"commit"`
}

type peaksDoc struct {
	Slices map[string][]peakSample `json:"slices"`
}

func readPeaks(ctx context.Context, store *gitstore.Store, suite string) (peaksDoc, string, error) {
	doc := peaksDoc{Slices: map[string][]peakSample{}}

	ref, ok, err := store.First(ctx, []string{PeaksRef(suite)})
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
	doc, _, err := readPeaks(ctx, store, suite)
	if err != nil {
		return nil, fmt.Errorf("peaks of %s: %w", suite, err)
	}

	out := map[string][]int64{}

	for name, samples := range doc.Slices {
		for _, s := range samples {
			out[name] = append(out[name], s.MB)
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

	measured := map[string]int64{}

	for _, s := range run.Suites {
		if s.Name != suite {
			continue
		}

		for _, sl := range s.Slices {
			if sl.PeakMB > 0 {
				measured[sl.display()] = sl.PeakMB
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

// AddPeaks adds a peak per slice measured at commit to a suite's peaks, the last PeaksKept of each; a
// slice that has one of the commit already keeps it.
func AddPeaks(ctx context.Context, store *gitstore.Store, suite, commit string, measured map[string]int64) error {
	var err error

	for range peakRetries {
		if err = addPeaks(ctx, store, suite, commit, measured); !errors.Is(err, gitstore.ErrStale) {
			break
		}
	}

	return err
}

func addPeaks(ctx context.Context, store *gitstore.Store, suite, full string, measured map[string]int64) error {
	doc, old, err := readPeaks(ctx, store, suite)
	if err != nil {
		return err
	}

	commit, added := short(full), false

	for _, name := range slices.Sorted(maps.Keys(measured)) {
		samples := doc.Slices[name]
		if slices.ContainsFunc(samples, func(s peakSample) bool { return s.Commit == commit }) {
			continue
		}

		samples = append(samples, peakSample{MB: measured[name], Commit: commit})
		doc.Slices[name], added = samples[max(len(samples)-PeaksKept, 0):], true
	}

	if !added {
		return nil
	}

	data, err := encode(doc)
	if err != nil {
		return err
	}

	_, err = store.Write(ctx, "peaks of "+suite, gitstore.Update{Ref: PeaksRef(suite), Old: old, Files: map[string][]byte{peaksFile: data}})

	return err
}
