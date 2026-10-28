package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errObservesOutside = errors.New("a correlator reads what the step that observes can't see")

func observeReadsText(name string, r plugin.ObserveRead) string {
	return fmt.Sprintf("%s config.%s = %q: the step that observes sees only the tree (%s/) and the coverage output (%s/), "+
		"and %q is under neither, so %s finds nothing there and every recorded slice can't be told",
		name, r.Key, r.Value, plugin.ObserveTree, plugin.ObserveCoverage, r.Value, name)
}

func observeReadsFix(r plugin.ObserveRead) string {
	return fmt.Sprintf("have the batch task write it into its coverage output and set config.%s to its place under %s/ "+
		"(or to a place in the tree, under %s/); in the Concourse runner, for example, the step that observes is "+
		"report batch-end, given only source, trigger, batches, result and coverage",
		r.Key, plugin.ObserveCoverage, plugin.ObserveTree)
}

func observeReadsFindings(ctx context.Context, cfg *config.Config, dir, cache string) []check.Finding {
	var out []check.Finding

	for _, p := range cfg.Plugins {
		if p.Kind != config.KindCorrelator {
			continue
		}

		d, err := pinnedDescribe(ctx, p, dir, cache)
		if err != nil {
			continue
		}

		for _, r := range d.ObserveReadsOutside(p.Config) {
			out = append(out, check.Finding{Kind: check.KindBadConfig, Path: config.FileName, Problem: observeReadsText(p.Name, r), Fix: observeReadsFix(r)})
		}
	}

	return out
}

// A run recording a suite such a correlator observes would lose its recording, so its plan fails.
func observeReadsProblems(p planning, cfg *config.Config, run string, suites []string) (lines []string, records bool) {
	if p.set == nil {
		return nil, false
	}

	for _, l := range p.set.Plugins {
		if l.Desc.Kind != plugin.KindCorrelator {
			continue
		}

		for _, r := range l.Desc.ObserveReadsOutside(l.Conf.Config) {
			lines = append(lines, observeReadsText(l.Conf.Name, r)+": "+observeReadsFix(r))
			records = records || recordsWith(cfg, run, suites, l.Conf.Name)
		}
	}

	return lines, records
}

func recordsWith(cfg *config.Config, run string, suites []string, name string) bool {
	if run == "" {
		return false
	}

	return slices.ContainsFunc(suites, func(s string) bool {
		return cfg.Records(run, s) && slices.Contains(cfg.CorrelatorsFor(s), name)
	})
}

func observeReadsError(lines []string) error {
	return fmt.Errorf("%w: %s", errObservesOutside, strings.Join(lines, "; "))
}

// Such a batch would run its slices and lose every recording.
func needObservable(cfg *config.Config, b *batchPlugins, suite string) error {
	if b == nil {
		return nil
	}

	var lines []string

	for _, c := range b.sets {
		if c.err != nil || !slices.Contains(cfg.CorrelatorsFor(suite), c.name) {
			continue
		}

		for _, l := range c.set.Plugins {
			for _, r := range l.Desc.ObserveReadsOutside(l.Conf.Config) {
				lines = append(lines, observeReadsText(l.Conf.Name, r)+": "+observeReadsFix(r))
			}
		}
	}

	if len(lines) == 0 {
		return nil
	}

	return observeReadsError(lines)
}

func correlatorWarnings(p planning, cfg *config.Config) []string {
	lines, _ := observeReadsProblems(p, cfg, "", nil)

	return append(noLinksWarnings(p), lines...)
}
