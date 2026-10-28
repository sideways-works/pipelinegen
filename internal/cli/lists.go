package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

func runLists(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"attach", "merge a run's recordings into a branch commit's lists", func(ctx context.Context, args []string) error { return listsAttach(ctx, args, stdout) }},
		{"show", "print the lists planning would use for a commit", func(ctx context.Context, args []string) error { return listsShow(ctx, args, stdout) }},
	})
}

func listsAttach(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c                       common
		run, change, commit, to string
	)

	fs := flag.NewFlagSet("lists attach", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &run, "run", "", "the run whose recordings are attached")
	textVar(fs, &change, "change", "", "the run's change (pr-<number> or a branch)")
	textVar(fs, &commit, "commit", "", "the commit the run tested, in full")
	textVar(fs, &to, "to", "", "the branch commit that gets the lists")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if run == "" || change == "" || commit == "" || to == "" {
		return fmt.Errorf("%w: --run, --change, --commit and --to are all needed", errUsage)
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	if _, ok := cfg.Runs[run]; !ok {
		return fmt.Errorf("%w: no run %q in the config", errUsage, run)
	}

	id := report.ID{Run: run, Change: change, Commit: commit}
	if err := id.Check(); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	plan, ok, err := readPlan(ctx, store, id)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: %s %s at %s has no plan, so nothing to attach", errNoPlan, run, change, short12(commit))
	}

	recordings, marks, err := runRecordings(ctx, store, id)
	if err != nil {
		return err
	}

	passed, err := passedSlices(ctx, store, id)
	if err != nil {
		return err
	}

	end, err := report.RunEnding(ctx, store, cfg, id)
	if err != nil {
		return err
	}

	shrink, whole := judged(recordings, passed, plan.All)

	got, err := lists.Attach{
		Repo: absRoot(c.root), Run: run, Change: change, Commit: commit, To: to,
		Recordings: recordings, CannotTell: passedMarks(marks, passed), Shrinkable: shrink, Whole: whole,
		SuitesPassed: end.Suites, Walk: cfg.ListsCommits(), Trusted: trustedRun(cfg, run, change),
	}.Do(ctx, store)
	if err != nil {
		return err
	}

	return say(stdout, "%s\n", attachedText(got, run+" "+change+" "+short12(commit)))
}

func attachedText(got *lists.Attached, who string) string {
	if got.Again {
		return fmt.Sprintf("lists of %s: %s attached already, nothing written", short12(got.To), who)
	}

	return fmt.Sprintf("lists of %s: %s merged into %s; %d slices, %d changed",
		short12(got.To), who, baseText(got.Base), got.Slices, got.Grown)
}

func passedSlices(ctx context.Context, store *gitstore.Store, id report.ID) (map[string]bool, error) {
	states, err := report.SliceStates(ctx, store, id)
	if err != nil {
		return nil, err
	}

	out := map[string]bool{}

	for key, st := range states {
		if st == report.Passed {
			out[key] = true
		}
	}

	return out, nil
}

func runRecordings(ctx context.Context, store *gitstore.Store, id report.ID) (recording.Lists, lists.Marks, error) {
	files, err := report.Recordings(ctx, store, id)
	if err != nil {
		return nil, nil, err
	}

	recs, marks, err := lists.Recordings(files)
	if err != nil {
		return nil, nil, fmt.Errorf("recordings of %s %s: %w", id.Run, id.Change, err)
	}

	return recs, marks, nil
}

func baseText(src lists.Source) string {
	switch {
	case src.Ref == "":
		return fmt.Sprintf("no earlier lists (none within %d commits)", src.Walk)
	case src.Back == 0:
		return "its own lists"
	case src.Back == 1:
		return "the lists of its parent " + short12(src.From)
	default:
		return fmt.Sprintf("the lists of %s, %d commits back", short12(src.From), src.Back)
	}
}

func short12(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}

	return commit
}

var errNoListsStore = errors.New("no store to read the lists from")

func listsShow(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c                    common
		commit, suite, slice string
		local                bool
	)

	fs := flag.NewFlagSet("lists show", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&local, "local", false, "read the lists from the local store `pipelinegen record` writes, not the remote")
	textVar(fs, &commit, "commit", "HEAD", "the commit whose lists it shows")
	textVar(fs, &suite, "suite", "", "only this suite")
	textVar(fs, &slice, "slice", "", "print this slice's units, one per line")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	if suite != "" {
		if _, err := pickSuites(cfg, suite); err != nil {
			return err
		}
	}

	root, err := filepath.Abs(c.root)
	if err != nil {
		return fmt.Errorf("root: %w", err)
	}

	full, ok, err := gitx.Repo{Dir: root}.Resolve(ctx, commit)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: no commit %s in %s", errUsage, commit, root)
	}

	from := listsRemote
	if local {
		from = listsLocal
	}

	store := listsStore(ctx, cfg, root, from, os.Stderr, "lists show")
	if store == nil {
		return errNoListsStore
	}

	defer closeStore(store, &err)

	all, src, err := lists.ForWithin(ctx, store, root, full, cfg.ListsCommits())
	if err != nil {
		return err
	}

	var b bytes.Buffer

	writeSource(&b, full, src)

	if err := writeSeeds(ctx, &b, store, src); err != nil {
		return err
	}

	if slice != "" {
		showSlice(&b, all, src, newestWhole(ctx, root, src.Replaced), suite, slice)
	} else {
		showCounts(&b, cfg, all, src.CannotTell, suite)
	}

	return emit(stdout, &b)
}

func writeSource(b *bytes.Buffer, commit string, src lists.Source) {
	fmt.Fprintf(b, "lists of %s: %s", short12(commit), baseText(src))

	if src.Ref == "" {
		b.WriteString(", so every recorded slice is bound to everything it links")
	}

	b.WriteString("\n")

	if len(src.Live) == 0 {
		b.WriteString("no live runs' recordings added\n")

		return
	}

	b.WriteString("with the recordings of the live runs:\n")

	for _, r := range src.Live {
		fmt.Fprintf(b, "  %s %s %s (%s)\n", r.Run, r.Change, r.Commit, plural(r.Slices, "slice"))
	}
}

// Seeded and baseline lists are as good as that machine's environment until a CI run of everything
// replaces them.
func writeSeeds(ctx context.Context, b *bytes.Buffer, store *gitstore.Store, src lists.Source) error {
	if src.Ref == "" {
		return nil
	}

	ref, ok, err := store.First(ctx, []string{src.Ref})
	if err != nil || !ok {
		return err
	}

	seeds, err := lists.Seeds(ctx, store, ref)
	if err != nil {
		return err
	}

	for _, a := range seeds {
		if a.Seed.Baseline {
			fmt.Fprintf(b, "a baseline of %s set by hand at %s (pipelinegen %s), not a run\n", a.Change, short12(a.Seed.From), a.Seed.Tool)

			continue
		}

		plugins := make([]string, 0, len(a.Seed.Plugins))
		for _, p := range a.Seed.Plugins {
			plugins = append(plugins, p.Name+" "+p.Version)
		}

		with := ""
		if len(plugins) > 0 {
			with = ", plugins " + strings.Join(plugins, ", ")
		}

		fmt.Fprintf(b, "seeded from a local recording of %s (%s, pipelinegen %s%s), not a CI run\n", short12(a.Seed.From), a.Change, a.Seed.Tool, with)
	}

	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return fmt.Sprintf("%d %ss", n, word)
}

func markText(ms []recording.Mark) string {
	parts := make([]string, 0, len(ms))

	for _, m := range ms {
		text := m.Plugin + " couldn't tell"
		if !m.Since.IsZero() {
			text += " since " + m.Since.UTC().Format(time.RFC3339)
		}

		if m.Reason != "" {
			text += " (" + secret.Redact(m.Reason) + ")"
		}

		parts = append(parts, text)
	}

	return "bound to everything until a recording tells again: " + strings.Join(parts, "; ")
}

func showCounts(b *bytes.Buffer, cfg *config.Config, all recording.Lists, marks lists.Marks, suite string) {
	bySuite := map[string][]string{}

	for _, key := range append(sortedKeys(all), sortedKeys(marks)...) {
		s, _, _ := strings.Cut(key, "/")
		if (suite == "" || s == suite) && !slices.Contains(bySuite[s], key) {
			bySuite[s] = append(bySuite[s], key)
		}
	}

	if len(bySuite) == 0 {
		b.WriteString("no lists\n")

		return
	}

	for _, s := range sortedKeys(bySuite) {
		keys := bySuite[s]
		slices.Sort(keys)

		note := ""
		if _, ok := cfg.Suites[s]; !ok {
			note = " (not in the config)"
		}

		fmt.Fprintf(b, "%s: %s%s\n", s, plural(len(keys), "slice"), note)

		for _, k := range keys {
			fmt.Fprintf(b, "  %s %d", strings.TrimPrefix(k, s+"/"), len(all[k]))

			if ms := marks[k]; len(ms) > 0 {
				b.WriteString(", " + markText(ms))
			}

			b.WriteString("\n")
		}
	}
}

func showSlice(b *bytes.Buffer, all recording.Lists, src lists.Source, newest map[string]string, suite, slice string) {
	found, marks := false, src.CannotTell

	keys := sortedKeys(all)
	for _, k := range sortedKeys(marks) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	for _, key := range keys {
		s, name, _ := strings.Cut(key, "/")
		if name != slice || suite != "" && s != suite {
			continue
		}

		found = true

		fmt.Fprintf(b, "%s %s: %s\n", s, name, plural(len(all[key]), "unit"))

		if ms := marks[key]; len(ms) > 0 {
			fmt.Fprintf(b, "  %s\n", markText(ms))
		}

		if at, last := src.Replaced[key], newest[s]; at != "" && last != "" && at != last {
			fmt.Fprintf(b, "  recorded whole at %s: the run of everything at %s didn't replace it (the slice didn't pass there, or couldn't be told)\n",
				short12(at), short12(last))
		}

		for _, id := range all[key] {
			fmt.Fprintf(b, "  %s\n", id)
		}
	}

	if !found {
		fmt.Fprintf(b, "no list for %s: it is bound to everything it links until a run records it\n", slice)
	}
}

// newestWhole is, per suite, the newest commit at which a run of everything replaced its lists. A slice
// whose list was replaced at an older one kept it through a run that failed it.
func newestWhole(ctx context.Context, root string, replaced map[string]string) map[string]string {
	bySuite := map[string][]string{}

	for key, at := range replaced {
		suite, _, _ := strings.Cut(key, "/")
		if !slices.Contains(bySuite[suite], at) {
			bySuite[suite] = append(bySuite[suite], at)
		}
	}

	out := map[string]string{}
	repo := gitx.Repo{Dir: root}

	for suite, commits := range bySuite {
		slices.Sort(commits)

		for _, c := range commits {
			if slices.ContainsFunc(commits, func(o string) bool {
				ok, err := repo.IsAncestor(ctx, o, c)

				return err != nil || !ok
			}) {
				continue
			}

			out[suite] = c

			break
		}
	}

	return out
}

func absRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}

	return root
}

// trustedRun tells whether a run's records may shape what later plans bind: a default-branch run, not
// a proposed change's. Every other run's recordings only add.
func trustedRun(cfg *config.Config, run, change string) bool {
	r, ok := cfg.Runs[run]

	return ok && r.Trigger.PullRequests == nil && change == cfg.Concourse.Branch
}
