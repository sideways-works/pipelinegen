package cli

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const checksSuite = `  checks:
    tag: slice-checks
    sample: 1
    steps:
      - {setup: command, with: {start: [sleep, "60"]}}
      - {run: command, take: ["//checks:lint"], with: {each: [lint]}}
      - {run: shell, take: ["//checks:vet"], with: {each: [vet]}}
`

const checksDriver = "plugins:\n  - {name: shell, kind: driver, repository: o/shell, release: {version: v1, sha256: " +
	"0000000000000000000000000000000000000000000000000000000000000000}}\n"

func configRepo(t *testing.T, bz []string) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("checks/BUILD.bazel", `filegroup(name = "lint", srcs = ["lint.txt"], tags = ["slice-checks"])
filegroup(name = "vet", srcs = ["vet.txt"], tags = ["slice-checks"])
`)
	r.Write("checks/lint.txt", "lint\n")
	r.Write("more/BUILD.bazel", "filegroup(name = \"one\", srcs = [\"one.spec.ts\"])\n")
	r.Write("more/one.spec.ts", "test('one')\n")
	r.Write("checks/vet.txt", "vet\n")

	text := readFile(t, filepath.Join(r.Dir, config.FileName))
	text = strings.Replace(text, "everything_on:\n  - pipelinegen.yaml\n", "sample_on: [pipelinegen.yaml]\neverything_on:\n", 1)
	text = strings.Replace(text, "runs:\n", checksSuite+"runs:\n", 1) + checksDriver
	text = "bazel: {bazelisk: bazelisk, startup_flags: [" + strings.Join(quoted(bz), ", ") + "]}\n" + text
	text = strings.Replace(text, "    suites: {e2e: {}, backend: {}}\n", "    suites: {e2e: {}, backend: {}, checks: {}}\n", 1)
	r.Write(config.FileName, text)
	r.Commit("checks run by steps")

	return r
}

func TestAConfigChangeSelectsByWhatItMeans(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t).Path
	cache := t.TempDir()
	bz := testrepo.Bazel(t, filepath.Join(cache, "tree"))
	r := configRepo(t, bz.StartupFlags)
	base := r.Git("rev-parse", "HEAD")
	text := readFile(t, filepath.Join(r.Dir, config.FileName))

	for _, c := range []struct {
		name, from, to string
		want           map[string]string
		sampled        []string
	}{
		{
			name: "a run step's settings", from: "with: {each: [vet]}", to: "with: {each: [vet, --strict]}",
			want: map[string]string{"checks": "checks.vet"},
		},
		{
			name: "a set-up step's settings", from: `with: {start: [sleep, "60"]}`, to: `with: {start: [sleep, "90"]}`,
			want: map[string]string{"checks": "checks.lint,checks.vet"},
		},
		{
			name: "a driver's pin", from: "release: {version: v1,", to: "release: {version: v2,",
			want: map[string]string{"checks": "checks.vet"},
		},
		{
			name: "a comment", from: "  checks:\n", to: "  # the repository's checks\n  checks:\n",
			want: map[string]string{},
		},
		{
			name: "a test pattern", from: `    tests: ["e2e/*.spec.ts"]`, to: `    tests: ["e2e/*.spec.ts", "more/*.spec.ts"]`,
			want: map[string]string{"specs": "more/one.spec.ts"},
		},
		{
			name: "a time limit", from: "    sample: 1\n", to: "    sample: 1\n    minutes: 30\n",
			sampled: []string{"backend", "checks", "e2e", "specs"},
		},
	} {
		r.Git("checkout", "-q", base)
		r.Write(config.FileName, strings.Replace(text, c.from, c.to, 1))
		head := r.Commit(c.name)

		cfg, err := config.Load(filepath.Join(r.Dir, config.FileName))
		if err != nil {
			t.Fatal(err)
		}

		p, err := affectedPlanUnder(t.Context(), cfg, r.Dir, base, head, false, cache, differ, listsNone, t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("%s: everything: %s", c.name, p.Reason)
		}

		if understood := c.sampled == nil; p.ConfigUnderstood != understood || c.name != "a comment" && len(p.Config) == 0 {
			t.Errorf("%s: config parts %+v, understood %v", c.name, p.Config, p.ConfigUnderstood)
		}

		var sampled []string

		for _, sp := range p.Suites {
			if strings.Contains(sp.Reason, "(sample_on ") || slices.ContainsFunc(sp.Narrowed, func(n affected.Narrowed) bool { return n.Narrowing == affected.SampledByConfig }) {
				sampled = append(sampled, sp.Suite)

				continue
			}

			if got := selectedNames(sp); got != c.want[sp.Suite] {
				t.Errorf("%s: %s selected %q, want %q", c.name, sp.Suite, got, c.want[sp.Suite])
			}

			for _, sl := range sp.Selected {
				if rs := sp.Reasons[sl.Label]; !slices.ContainsFunc(rs, func(r affected.Reason) bool {
					return r.Kind == affected.KindConfig || r.Kind == affected.KindPlugin || r.Kind == affected.KindNewSlice && r.What == affected.AddedSliceReason
				}) {
					t.Errorf("%s: %s has no reason of the config: %+v", c.name, sl.Name, rs)
				}
			}
		}

		slices.Sort(sampled)

		if !slices.Equal(sampled, c.sampled) {
			t.Errorf("%s: sample_on sampled %v, want %v", c.name, sampled, c.sampled)
		}
	}
}

func quoted(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, strconv.Quote(s))
	}

	return out
}

func TestAConfigReasonReads(t *testing.T) {
	r := affected.Reason{Kind: affected.KindConfig, What: "suites.checks.steps[1] (run command), which takes it, changed"}
	if got := reasonLine(r); got != "the config: suites.checks.steps[1] (run command), which takes it, changed" {
		t.Errorf("why: %q", got)
	}

	if got := reasonLine(affected.Reason{Kind: affected.KindPlugin, What: "shell"}); got != "the entry of plugin shell changed" {
		t.Errorf("why: %q", got)
	}
}
