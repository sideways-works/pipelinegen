package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/durations"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

type files []string

func (f *files) String() string { return strings.Join(*f, ",") }

func (f *files) Set(v string) error {
	*f = append(*f, v)

	return nil
}

type durationsFlags struct {
	common

	suite, param, pwDir string
	run, change, commit string
	goJSON, playwright  files
}

func (f *durationsFlags) register(fs *flag.FlagSet) {
	f.common.register(fs)
	textVar(fs, &f.suite, "suite", "", "the suite whose durations file it writes")
	fs.Var(&f.goJSON, "go-json", "a file of `go test -json` output (repeatable)")
	fs.Var(&f.playwright, "playwright", "a report of Playwright's JSON reporter (repeatable)")
	textVar(fs, &f.param, "param", "", "the parameter set the measured run ran with")
	textVar(fs, &f.run, "run", "", "take each slice's time from this run's refs, as its batches' journals timed it")
	textVar(fs, &f.change, "change", "", "the change of --run")
	textVar(fs, &f.commit, "commit", "", "the commit of --run (default: the change's newest)")
	textVar(fs, &f.pwDir, "playwright-base", "", "the directory, from the root, that the Playwright reports' file names are relative to (default: found from the report)")
}

func (f *durationsFlags) check() error {
	if f.suite == "" || len(f.goJSON) == 0 && len(f.playwright) == 0 && f.run == "" {
		return fmt.Errorf("%w: --suite and a source (--go-json, --playwright or --run) are needed", errUsage)
	}

	if (f.run == "") != (f.change == "") {
		return fmt.Errorf("%w: --run and --change go together", errUsage)
	}

	return nil
}

func runDurations(ctx context.Context, args []string, stdout io.Writer) error {
	var f durationsFlags

	fs := flag.NewFlagSet("durations", flag.ContinueOnError)
	f.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := f.check(); err != nil {
		return err
	}

	c, suite, run := f.common, f.suite, f.run

	cfg, err := c.load()
	if err != nil {
		return err
	}

	if _, err := pickSuites(cfg, suite); err != nil {
		return err
	}

	tracked, err := trackedFiles(ctx, c.root)
	if err != nil {
		return err
	}

	reports, err := readReports(f.goJSON, f.playwright)
	if err != nil {
		return err
	}

	reports.Param, reports.PlaywrightBase = f.param, f.pwDir

	if run != "" {
		if reports.Seconds, err = runSeconds(ctx, cfg, run, f.change, f.commit, suite); err != nil {
			return err
		}
	}

	read := func(p string) ([]byte, error) {
		return os.ReadFile(filepath.Join(c.root, filepath.FromSlash(p)))
	}

	got, err := durations.Record(cfg, durations.Repo{Files: tracked, Read: read}, suite, reports)
	if err != nil {
		return err
	}

	before, err := writeDurations(c.root, got)
	if err != nil {
		return err
	}

	after, err := durations.Parse(got.Content)
	if err != nil {
		return err
	}

	var b bytes.Buffer

	writeChanges(&b, got.Path, before, after)
	writeUnknown(&b, suite, got.Unknown)

	return emit(stdout, &b)
}

func writeUnknown(b *bytes.Buffer, suite string, unknown []string) {
	if len(unknown) == 0 {
		return
	}

	fmt.Fprintf(b, "no test file of %s declares these tests the reports timed:\n", suite)

	for _, t := range unknown {
		fmt.Fprintf(b, "  %s\n", t)
	}
}

func runSeconds(ctx context.Context, cfg *config.Config, run, change, commit, suite string) (_ map[string]float64, err error) {
	store := openStore(cfg)
	defer closeStore(store, &err)

	id, err := keptRun(ctx, store, run, change, commit)
	if err != nil {
		return nil, err
	}

	return report.Seconds(ctx, store, id, suite)
}

func readReports(goJSON, playwright []string) (durations.Reports, error) {
	var reports durations.Reports

	for _, group := range []struct {
		paths []string
		into  *[]io.Reader
	}{{goJSON, &reports.GoJSON}, {playwright, &reports.Playwright}} {
		for _, p := range group.paths {
			data, err := os.ReadFile(p) //nolint:gosec // a report the person named
			if err != nil {
				return reports, fmt.Errorf("report: %w", err)
			}

			*group.into = append(*group.into, bytes.NewReader(data))
		}
	}

	return reports, nil
}

func writeDurations(root string, got *durations.Recorded) (*durations.File, error) {
	path := filepath.Join(root, filepath.FromSlash(got.Path))

	before, err := durations.Read(path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("durations: %w", err)
	}

	if err := os.WriteFile(path, got.Content, 0o600); err != nil {
		return nil, fmt.Errorf("durations: %w", err)
	}

	return before, nil
}

func writeChanges(b *bytes.Buffer, path string, before, after *durations.File) {
	var added, removed, changed []string

	for f, per := range after.Files {
		old, ok := before.Files[f]

		switch {
		case !ok:
			added = append(added, fmt.Sprintf("  + %s %s", f, seconds(per)))
		case !samePer(old, per):
			changed = append(changed, fmt.Sprintf("  ~ %s %s, was %s", f, seconds(per), seconds(old)))
		}
	}

	for f, per := range before.Files {
		if _, ok := after.Files[f]; !ok {
			removed = append(removed, fmt.Sprintf("  - %s %s", f, seconds(per)))
		}
	}

	if len(added)+len(removed)+len(changed) == 0 {
		fmt.Fprintf(b, "%s: no change, %s\n", path, plural(len(after.Files), "test file"))

		return
	}

	fmt.Fprintf(b, "%s: %d added, %d changed, %d removed, %s\n", path, len(added), len(changed), len(removed), plural(len(after.Files), "test file"))

	for _, group := range [][]string{added, changed, removed} {
		slices.Sort(group)

		for _, line := range group {
			b.WriteString(line + "\n")
		}
	}
}

func samePer(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}

	return true
}

func seconds(per map[string]float64) string {
	if v, ok := per[durations.Plain]; ok && len(per) == 1 {
		return fmt.Sprintf("%.1fs", v)
	}

	keys := make([]string, 0, len(per))
	for k := range per {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.1fs", k, per[k]))
	}

	return "(" + strings.Join(parts, ", ") + ")"
}

func trackedFiles(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z") //nolint:gosec // git in the root the command was given
	cmd.Env = secret.HardenGit(os.Environ())

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", root, err)
	}

	var fs []string

	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			fs = append(fs, p)
		}
	}

	return fs, nil
}
