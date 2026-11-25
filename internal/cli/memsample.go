package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/durations"
	"github.com/sideways-works/pipelinegen/internal/report"
)

var errSampleRefused = errors.New("memory sample refused")

// memorySampleRun runs a sample of a suite's slices on this machine, each in a batch of its own in its
// image, measures each one's peak as a batch on CI does, and records the peak with headroom added as
// the slice's, so a suite that waits for memory predicts from them before CI measured it.
func memorySampleRun(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		f        localRunFlags
		n        int
		headroom int
		dryRun   bool
	)

	fs := flag.NewFlagSet("memory sample", flag.ContinueOnError)
	f.register(fs)
	fs.IntVar(&n, "slices", 8, "how many of the suite's slices to measure, spread over their durations; --slice names them instead")
	fs.IntVar(&headroom, "headroom", 25, "the percentage added to each measured peak before it is recorded")
	fs.BoolVar(&dryRun, "dry-run", false, "measure and say what would be recorded, writing nothing to the store")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := sampleArgs(&f, n, headroom); err != nil {
		return err
	}

	suite := f.suites[0]
	f.all = true

	ctx = withFill(ctx, "memory sample", *f.lp.noFill)
	f.root = absRoot(f.root)
	f.lp.plugins = pluginsIn(f.root, f.lp.plugins)

	cfg, plan, _, err := f.plan(ctx)
	if err != nil {
		return err
	}

	if !dryRun && !storeCredential(cfg) {
		return fmt.Errorf("%w: the store's writing credential isn't in the environment (%s); the peaks are the shared store's, so it needs it, or --dry-run", errSampleRefused, storeCredentialName(cfg))
	}

	sp, sample, err := f.sample(cfg, plan, suite, n)
	if err != nil {
		return err
	}

	measured, err := f.measure(ctx, plan.Head, sp, sample, headroom, stdout)
	if err != nil {
		return err
	}

	if len(measured) == 0 {
		return fmt.Errorf("%w: no slice of the sample was measured", errSampleRefused)
	}

	if dryRun {
		return say(stdout, "dry run: %d peaks of %s not recorded\n", len(measured), suite)
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	if err := report.AddPeaks(ctx, store, suite, plan.Head, measured); err != nil {
		return fmt.Errorf("memory sample: %w", err)
	}

	return say(stdout, "recorded %d peaks of %s at %s, each the measured peak and %d%%\n", len(measured), suite, short12(plan.Head), headroom)
}

func sampleArgs(f *localRunFlags, n, headroom int) error {
	switch {
	case len(f.suites) != 1:
		return fmt.Errorf("%w: --suite names the one suite to measure", errUsage)
	case n < 1 || headroom < 0:
		return fmt.Errorf("%w: --slices is at least 1 and --headroom at least 0", errUsage)
	case f.images.noImage:
		return fmt.Errorf("%w: --no-image runs the batch in this machine's own cgroup, which holds more than the batch: nothing would be measured", errSampleRefused)
	}

	return nil
}

// sample is the suite's plan, every slice selected, and the slices to measure: those --slice names, or
// n spread over their durations.
func (f *localRunFlags) sample(cfg *config.Config, plan *affected.Plan, suite string, n int) (affected.SuitePlan, []bazel.Slice, error) {
	explicit := len(f.slices) > 0

	if err := f.only(plan); err != nil {
		return affected.SuitePlan{}, nil, err
	}

	todo, err := f.runnable(cfg, plan)
	if err != nil {
		return affected.SuitePlan{}, nil, err
	}

	i := slices.IndexFunc(todo, func(sp affected.SuitePlan) bool { return sp.Suite == suite })
	if i < 0 || len(todo[i].Selected) == 0 {
		return affected.SuitePlan{}, nil, fmt.Errorf("%w: suite %s has no slice to run here", errSampleRefused, suite)
	}

	sp := todo[i]
	if explicit {
		return sp, sp.Selected, nil
	}

	return sp, spread(f.root, cfg.Suites[suite], sp.Selected, n), nil
}

// measure runs each slice of sample in a batch of its own, in its image, and returns its peak with
// headroom added: alone in its batch, a slice's peak is its own, its set-up included.
func (f *localRunFlags) measure(ctx context.Context, head string, sp affected.SuitePlan, sample []bazel.Slice, headroom int, stdout io.Writer) (map[string]int64, error) {
	data, src, err := runConfigData(ctx, &f.common, f.root, head)
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "pipelinegen-sample-")
	if err != nil {
		return nil, fmt.Errorf("memory sample: %w", err)
	}

	defer os.RemoveAll(work) //nolint:errcheck // a temporary directory

	r := &localSuiteRun{data: data, src: src, root: f.root, work: work, temp: true, plugins: f.lp.plugins, head: head, out: stdout, images: f.images, common: &f.common, measure: true}

	measured := map[string]int64{}

	for i, sl := range sample {
		one := sp
		one.Selected = []bazel.Slice{sl}

		r.work = filepath.Join(work, strconv.Itoa(i))

		t, err := r.suite(ctx, one, false)
		if err != nil {
			return nil, fmt.Errorf("slice %s: %w", sl.Name, err)
		}

		peak, ok := t.peaks[sl.Name]

		switch {
		case t.short != "":
			warnf(stdout, "%s: not run: %s\n", sl.Name, t.short)
		case !ok:
			warnf(stdout, "%s: %s, no peak measured\n", sl.Name, t.states[sl.Name])
		default:
			measured[sl.Name] = peak * int64(100+headroom) / 100
			warnf(stdout, "%s: %s, peak %d MB, recorded as %d MB\n", sl.Name, t.states[sl.Name], peak, measured[sl.Name])
		}
	}

	return measured, nil
}

// spread picks n of the selected slices spread over their recorded durations, the longest first, so the
// sample holds the heaviest and the lightest; without durations, spread over their names.
func spread(root string, s config.Suite, selected []bazel.Slice, n int) []bazel.Slice {
	secs := map[string]float64{}

	if s.Durations != "" {
		if file, err := durations.Read(filepath.Join(root, s.Durations)); err == nil {
			for _, sl := range selected {
				for _, src := range sl.Sources {
					if v, ok := file.Seconds(src, nil); ok {
						secs[sl.Name] += v
					}
				}
			}
		}
	}

	sorted := slices.Clone(selected)
	slices.SortFunc(sorted, func(a, b bazel.Slice) int {
		return cmp.Or(cmp.Compare(secs[b.Name], secs[a.Name]), cmp.Compare(a.Name, b.Name))
	})

	if n >= len(sorted) {
		return sorted
	}

	if n == 1 {
		return sorted[:1]
	}

	out := make([]bazel.Slice, 0, n)
	for i := range n {
		out = append(out, sorted[i*(len(sorted)-1)/(n-1)])
	}

	return out
}
