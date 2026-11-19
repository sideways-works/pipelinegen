package proof

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The config rows plan changes of the config on a setting adding a suite of two step-run checks; the
// suite is in no run, so only these plans read it.
const (
	checkLint     = "checks/lint_check.sh"
	checkVet      = "checks/vet_check.sh"
	checksSetting = "steps"
	configMixed   = "mixed"
)

// checksSuite is the suite the setting adds; checksDriver its driver
// entry, pinned by a release no plan fetches.
const (
	checksSuite = `  checks:
    tests: ["checks/*_check.sh"]
    owner: "//{dir}:{stem}"
    sample: 1
    steps:
      - {setup: command, with: {start: ["true"]}}
      - {run: command, take: [checks/lint_check.sh], with: {each: [sh, "{source}"]}}
      - {run: shell, take: [checks/vet_check.sh], with: {script: "sh {source}"}}
`
	checksDriver = `  - name: shell
    kind: driver
    repository: sideways-works/shell-driver
    release: {version: v0.1.0, sha256: 0000000000000000000000000000000000000000000000000000000000000000}
`
)

var configCases = []struct {
	name, from, to string
}{
	// a run step's settings: the slice it takes
	{"run-step", `with: {each: [sh, "{source}"]}`, `with: {each: [sh, -e, "{source}"]}`},
	// a set-up step's settings: every slice of the suite
	{"setup-step", `with: {start: ["true"]}`, `with: {start: ["true", "again"]}`},
	// a driver entry's pin: the slices its steps run, through plugin-entry
	{"driver-pin", "release: {version: v0.1.0,", "release: {version: v0.1.1,"},
	// a comment: nothing
	{"comment", "  checks:\n", "  # two checks, run by steps\n  checks:\n"},
	// a time limit, which the planner doesn't understand, beside the run step: the config is a sample_on
	// file again, and the step's slice runs whatever the sample picks
	{configMixed, `    sample: 1
    steps:`, `    sample: 1
    minutes: 30
    steps:`},
}

var configExpected = map[string]string{
	"run-step":   checkLint,
	"setup-step": checkLint + "," + checkVet,
	"driver-pin": checkVet,
	"comment":    "",
}

func (r *run) configRows(ctx context.Context, m *planner) error {
	if err := m.fx(ctx, "checkout", "-q", "-B", "setting/"+checksSetting, r.c0); err != nil {
		return err
	}

	files := map[string]string{
		"checks/BUILD.bazel": `filegroup(name = "lint_check", srcs = ["lint_check.sh"])

filegroup(name = "vet_check", srcs = ["vet_check.sh"])
`,
		checkLint: "#!/bin/sh\necho lint\n",
		checkVet:  "#!/bin/sh\necho vet\n",
	}

	for name, text := range files {
		path := filepath.Join(m.fixture, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("config rows: %w", err)
		}

		if err := writeFile(path, text); err != nil {
			return err
		}
	}

	if err := m.editText("pipelinegen.yaml", func(s string) string {
		s = strings.Replace(s, "suites:\n", "suites:\n"+checksSuite, 1)

		return strings.Replace(s, "plugins:\n", "plugins:\n"+checksDriver, 1)
	}); err != nil {
		return err
	}

	if err := m.fx(ctx, "add", "-A"); err != nil {
		return err
	}

	if err := m.fx(ctx, "commit", "-qm", checksSetting); err != nil {
		return err
	}

	for _, c := range configCases {
		branch := "config/" + c.name

		if err := m.fx(ctx, "checkout", "-q", "-B", branch, "setting/"+checksSetting); err != nil {
			return err
		}

		edit := func(s string) string { return strings.Replace(s, c.from, c.to, 1) }
		if c.name == configMixed {
			edit = func(s string) string {
				s = strings.Replace(s, c.from, c.to, 1)

				return strings.Replace(s, configCases[0].from, configCases[0].to, 1)
			}
		}

		if err := m.editText("pipelinegen.yaml", edit); err != nil {
			return err
		}

		if err := m.fx(ctx, "commit", "-qam", c.name); err != nil {
			return err
		}

		if err := m.plan(ctx, "config-"+c.name, "setting/"+checksSetting, branch, true); err != nil {
			return err
		}
	}

	return nil
}

var errNoEdit = errors.New("the edit changed nothing")

// editText changes a fixture file as a whole; an edit that changes nothing is an error, so a row can't
// plan a change it never made.
func (p *planner) editText(file string, edit func(string) string) error {
	path := filepath.Join(p.fixture, filepath.FromSlash(file))

	data, err := os.ReadFile(path) //nolint:gosec // the fixture's own file
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}

	text := edit(string(data))
	if text == string(data) {
		return fmt.Errorf("%w: %s", errNoEdit, file)
	}

	return writeFile(path, text)
}

func (r *run) configTable(t *strings.Builder) {
	for _, c := range configCases {
		p := r.readPlan(filepath.Join(r.work, "plans", "config-"+c.name+".json"))
		got := p.Selected()

		if c.name == configMixed {
			r.check(t, "config-"+c.name, "selected "+or(got, nothing)+"; "+p.NarrowedShown(),
				has(got, checkLint) && !has(got, checkVet) && p.Count() == 2 && p.Sampled("sample_on") > 0)

			continue
		}

		r.check(t, "config-"+c.name, "selected "+or(got, nothing)+"; "+p.NarrowedShown(), got == configExpected[c.name] && p.Sampled("sample_on") == 0)
	}
}
