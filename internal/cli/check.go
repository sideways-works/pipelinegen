package cli

import (
	"bytes"
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/check"
)

func runCheck(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c               common
		staged, summary bool
		pluginsDir      string
	)

	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&staged, "staged", false, "check what the next commit holds (git's index) and report what it touches")
	pathVar(fs, &pluginsDir, "plugins", "", "the directory holding the configured plugins the check asks, one it lacks fetched from its pinned release (default: tools/plugins under the root)", reads)
	fs.BoolVar(&summary, "summary", false, "print how many findings of each kind each top-level directory holds, instead of each finding")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	c.spellings = true

	cfg, err := c.load()
	if err != nil {
		return err
	}

	for _, n := range cfg.Notices {
		warnf(os.Stderr, "pipelinegen check: config: %s\n", n)
	}

	findings, err := check.Run(ctx, cfg, c.root, staged, checkOptions())
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	if pluginsDir == "" {
		pluginsDir = filepath.Join(absRoot(c.root), "tools", "plugins")
	}

	findings = judgedByPlan(ctx, cfg, absOr(pluginsDir), defaultCache(), findings)
	findings = append(findings, unwalkableFindings(ctx, cfg, absOr(pluginsDir), defaultCache())...)
	findings = append(findings, observeReadsFindings(ctx, cfg, absOr(pluginsDir), defaultCache())...)
	findings = append(findings, stepsFindings(ctx, cfg, absRoot(c.root), absOr(pluginsDir), defaultCache())...)
	findings = append(findings, runnerFindings(ctx, cfg, c.root, absOr(pluginsDir))...)

	// The hook checks a commit, not the pipeline: token warnings belong to the full check and each pull
	// request's plan.
	if !staged {
		findings = append(findings, singleTokenFinding(cfg)...)
		findings = append(findings, writingSecretFindings(cfg)...)
	}

	var b bytes.Buffer

	if summary {
		summarize(&b, findings)
	} else {
		for _, f := range findings {
			fmt.Fprintln(&b, f.String())
		}
	}

	if err := emit(stdout, &b); err != nil {
		return err
	}

	// A warning names a deprecated line: it fails nothing yet.
	if failed := check.Failures(findings); len(failed) > 0 {
		return fmt.Errorf("%s: %w", plural(len(failed), "finding"), errNotPassed)
	}

	return nil
}

// With the walk no deps repository is written, so its setup lines are left over.
func checkOptions() check.Options {
	return check.Options{NoDepsRepo: outward()}
}

// summarize writes one `<count>\t<kind>\t<directory>/` line per kind and top-level directory, most first.
func summarize(w *bytes.Buffer, findings []check.Finding) {
	type group struct{ kind, dir string }

	counts := map[group]int{}

	for _, f := range findings {
		dir, _, nested := strings.Cut(f.Path, "/")
		if !nested {
			dir = "."
		} else {
			dir += "/"
		}

		kind := f.Kind
		if f.Warning {
			kind += " (warning)"
		}

		counts[group{kind, dir}]++
	}

	groups := slices.Collect(maps.Keys(counts))
	slices.SortFunc(groups, func(a, b group) int {
		if c := cmp.Compare(counts[b], counts[a]); c != 0 {
			return c
		}

		return cmp.Or(strings.Compare(a.kind, b.kind), strings.Compare(a.dir, b.dir))
	})

	for _, g := range groups {
		fmt.Fprintf(w, "%d\t%s\t%s\n", counts[g], g.kind, g.dir)
	}

	fmt.Fprintf(w, "%s\n", plural(len(findings), "finding"))
}
