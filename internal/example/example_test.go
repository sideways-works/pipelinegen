// Package example_test checks that the example repository in example/ plans
// the way its comments and the documentation say.
package example_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/cli"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func exampleDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join("..", "..", "example"))
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func newRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.NewFrom(t, exampleDir(t))

	cfg, err := os.ReadFile(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	const line = "  bazelisk: bazelisk\n"
	if !strings.Contains(string(cfg), line) {
		t.Fatalf("the example's config has no %q", line)
	}

	flags, err := json.Marshal(testrepo.Bazel(t, r.Dir).StartupFlags)
	if err != nil {
		t.Fatal(err)
	}

	r.Write(config.FileName, strings.Replace(string(cfg), line, line+"  startup_flags: "+string(flags)+"\n", 1))
	r.Commit("startup flags for the test")

	return r
}

func run(t *testing.T, args ...string) string {
	t.Helper()

	var stdout, stderr strings.Builder

	if code := cli.Run(t.Context(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("pipelinegen %s: exit %d\n%s", strings.Join(args, " "), code, stderr.String())
	}

	return stdout.String()
}

func TestExampleConfig(t *testing.T) {
	cfg, err := config.Load(filepath.Join(exampleDir(t), config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.SuiteNames(); !slices.Equal(got, []string{"smoke", "unit"}) {
		t.Fatalf("suites %v", got)
	}

	if got := cfg.RunNames(); !slices.Equal(got, []string{"main", "nightly", "pr"}) {
		t.Fatalf("runs %v", got)
	}

	pr := cfg.Runs["pr"]
	if pr.Trigger.PullRequests == nil || pr.Compare != config.CompareMergedTarget || pr.Select != config.SelectAffected || pr.Report.Comment == nil {
		t.Fatalf("pr: %+v", pr)
	}

	if m := cfg.Runs["main"]; m.Trigger.Branch != "main" || m.Compare != config.CompareLastPassed || m.Select != config.SelectAffected {
		t.Fatalf("main: %+v", m)
	}

	if n := cfg.Runs["nightly"]; n.Trigger.Timer == nil || n.Select != config.SelectAll || !n.Record {
		t.Fatalf("nightly: %+v", n)
	}

	if cfg.Suites["unit"].Bind != config.BindGraph || cfg.Suites["smoke"].Bind != config.BindGraph {
		t.Fatal("unit and smoke must be bound by the graph")
	}

	if cfg.Suites["smoke"].RunOn.SerialGroup == "" {
		t.Fatal("smoke has no serial group")
	}

	for _, s := range cfg.Suites {
		for _, task := range []string{s.Prepare, s.RunSlice} {
			if task == "" {
				continue
			}

			if _, err := os.Stat(filepath.Join(exampleDir(t), task)); err != nil {
				t.Errorf("task file: %v", err)
			}
		}
	}
}

func TestExampleSlices(t *testing.T) {
	r := newRepo(t)

	var got []struct {
		Suite  string `json:"suite"`
		Slices []struct {
			Name    string   `json:"name"`
			Sources []string `json:"sources"`
		} `json:"slices"`
	}

	if err := json.Unmarshal([]byte(run(t, "slices", "-root", r.Dir, "-json")), &got); err != nil {
		t.Fatal(err)
	}

	names := map[string][]string{}

	for _, s := range got {
		for _, sl := range s.Slices {
			if !slices.Equal(sl.Sources, []string{sl.Name}) {
				t.Errorf("%s: sources %v, want only the file", sl.Name, sl.Sources)
			}

			names[s.Suite] = append(names[s.Suite], sl.Name)
		}
	}

	want := map[string][]string{
		"smoke": {"words/count_smoke.sh", "words/upper_smoke.sh"},
		"unit":  {"calc/add_test.sh", "calc/lib_test.sh", "calc/mul_test.sh", "text/upper_test.sh"},
	}

	if !equal(names, want) {
		t.Fatalf("slices %v, want %v", names, want)
	}
}

type change struct {
	name    string
	edit    func(r *testrepo.Repo)
	want    map[string][]string
	removed map[string][]string
	all     string
}

func replace(t *testing.T, r *testrepo.Repo, file, old, repl string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(r.Dir, file))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), old) {
		t.Fatalf("%s holds no %q", file, old)
	}

	r.Write(file, strings.Replace(string(data), old, repl, 1))
}

func appendTo(t *testing.T, r *testrepo.Repo, file, text string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(r.Dir, file))
	if err != nil {
		t.Fatal(err)
	}

	r.Write(file, string(data)+text)
}

func ownerTarget(name string) string {
	return "\nfilegroup(\n    name = \"" + name + "\",\n    srcs = [\n        \"" + name + ".sh\",\n        \":lib\",\n    ],\n)\n"
}

func changes(t *testing.T) []change {
	t.Helper()

	return []change{
		{
			name: "a test added to a test file runs that file",
			edit: func(r *testrepo.Repo) {
				appendTo(t, r, "calc/add_test.sh", "\ntest_add_zero() {\n\t[ \"$(add 0 0)\" = 0 ] || fail \"add 0 0\"\n}\n")
			},
			want: map[string][]string{"unit": {"calc/add_test.sh"}},
		},
		{
			name: "a test removed from a test file runs that file",
			edit: func(r *testrepo.Repo) {
				replace(t, r, "calc/mul_test.sh", "\ntest_mul_zero() {\n\t[ \"$(mul 9 0)\" = 0 ] || fail \"mul 9 0 = $(mul 9 0), want 0\"\n}\n", "")
			},
			want: map[string][]string{"unit": {"calc/mul_test.sh"}},
		},
		{
			name: "a library runs the test files that depend on it",
			edit: func(r *testrepo.Repo) {
				replace(t, r, "calc/lib.sh", "$(($1 + $2))", "$(( $1 + $2 ))")
			},
			want: map[string][]string{"unit": {"calc/add_test.sh", "calc/lib_test.sh", "calc/mul_test.sh"}},
		},
		{
			name: "an added test file runs",
			edit: func(r *testrepo.Repo) {
				r.Write("calc/sub_test.sh", ". ./lib.sh\n\ntest_add_large() {\n\t[ \"$(add 1000 1)\" = 1001 ] || fail \"add 1000 1\"\n}\n")
				appendTo(t, r, "calc/BUILD.bazel", ownerTarget("sub_test"))
			},
			want: map[string][]string{"unit": {"calc/sub_test.sh"}},
		},
		{
			name: "a removed test file is named as removed",
			edit: func(r *testrepo.Repo) {
				r.Git("rm", "-q", "calc/lib_test.sh")
				replace(t, r, "calc/BUILD.bazel", ownerTarget("lib_test"), "")
			},
			want:    map[string][]string{},
			removed: map[string][]string{"unit": {"calc/lib_test.sh"}},
		},
		{
			name: "a moved test file runs at its new path",
			edit: func(r *testrepo.Repo) {
				r.Git("mv", "calc/add_test.sh", "calc/sum_test.sh")
				replace(t, r, "calc/BUILD.bazel", ownerTarget("add_test"), ownerTarget("sum_test"))
			},
			want:    map[string][]string{"unit": {"calc/sum_test.sh"}},
			removed: map[string][]string{"unit": {"calc/add_test.sh"}},
		},
		{
			name: "another package's library runs only its test files",
			edit: func(r *testrepo.Repo) {
				replace(t, r, "text/lib.sh", "'[:lower:]' '[:upper:]'", "a-z A-Z")
			},
			want: map[string][]string{"unit": {"text/upper_test.sh"}},
		},
		{
			// By the graph alone, both test files that run against the
			// server; once the smoke suite has recordings, only count.
			name: "the Go server runs the smoke test files",
			edit: func(r *testrepo.Repo) {
				replace(t, r, "words/words.go", "return len(strings.Fields(s))", "n := len(strings.Fields(s))\n\n\treturn n")
			},
			want: map[string][]string{"smoke": {"words/count_smoke.sh", "words/upper_smoke.sh"}},
		},
		{
			name: "the build setup runs everything",
			edit: func(r *testrepo.Repo) {
				appendTo(t, r, "ci/root.yml", "# changed\n")
			},
			all: "ci/root.yml changed (everything_on ci/**)",
		},
	}
}

const noSelectors = "learn's stub did not run"

func selected(p *affected.Plan) map[string][]string {
	out := map[string][]string{}

	for _, sp := range p.Suites {
		for _, sl := range sp.Selected {
			out[sp.Suite] = append(out[sp.Suite], sl.Name)
		}
	}

	return out
}

func removedOf(p *affected.Plan) map[string][]string {
	out := map[string][]string{}

	for _, sp := range p.Suites {
		for _, l := range sp.Removed {
			name := strings.TrimPrefix(l, "@pipelinegen_deps//slices:")
			out[sp.Suite] = append(out[sp.Suite], name)
		}
	}

	return out
}

func equal(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}

	return true
}

func plan(t *testing.T, r *testrepo.Repo, cache, bd string, args ...string) (*affected.Plan, string) {
	t.Helper()

	out := run(t, append([]string{"affected", "-root", r.Dir, "-cache", cache, "-bazel-diff", bd, "-json"}, args...)...)

	var p affected.Plan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}

	return &p, out
}

func TestExampleAffected(t *testing.T) {
	bd := bazeldifftest.Tool(t).Path

	r := newRepo(t)
	base := r.Git("rev-parse", "HEAD")
	cache := t.TempDir()

	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	p, _ := plan(t, r, cache, bd, "-head", base)
	if !p.All || p.Reason != "nothing to compare with" || len(slices.Concat(selected(p)["unit"], selected(p)["smoke"])) != 6 {
		t.Fatalf("no base: %+v", p)
	}

	var (
		first struct{ head, out string }
	)

	for _, c := range changes(t) {
		r.Git("checkout", "-q", "-B", "case-"+strings.ReplaceAll(c.name, " ", "-"), base)
		c.edit(r)
		head := r.Commit(c.name)

		p, out := plan(t, r, cache, bd, "-base", base, "-head", head)
		if first.head == "" {
			first.head, first.out = head, out
		}

		if c.all != "" {
			if !p.All || p.Reason != c.all {
				t.Errorf("%s: all %v, reason %q, want %q", c.name, p.All, p.Reason, c.all)
			}

			continue
		}

		if reason, ok := runsWhole(p); ok {
			t.Errorf("%s: a suite of test files ran whole: %s", c.name, reason)

			continue
		}

		if p.All || !equal(selected(p), c.want) {
			t.Errorf("%s: selected %v (all %v: %s), want %v", c.name, selected(p), p.All, p.Reason, c.want)
		}

		if c.removed != nil && !equal(removedOf(p), c.removed) {
			t.Errorf("%s: removed %v, want %v", c.name, removedOf(p), c.removed)
		}
	}

	// The plan depends on the two commits alone: planned again from
	// nothing (a new cache and work tree), it is the same to the byte.
	again := t.TempDir()
	testrepo.Bazel(t, filepath.Join(again, "tree"))

	if _, out := plan(t, r, again, bd, "-base", base, "-head", first.head); out != first.out {
		t.Errorf("planned again:\n%s\nfirst:\n%s", out, first.out)
	}
}

func runsWhole(p *affected.Plan) (string, bool) {
	for _, sp := range p.Suites {
		if sp.All && strings.Contains(sp.Reason, noSelectors) {
			return sp.Reason, true
		}
	}

	return "", false
}

func TestRepositoryConfig(t *testing.T) {
	cfg, err := config.Load(filepath.Join(exampleDir(t), "..", config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.SuiteNames(); !slices.Equal(got, []string{"go", "go-whole", "lint", "proof", "smoke", "unit"}) {
		t.Fatalf("suites %v", got)
	}

	for _, name := range []string{"smoke", "unit"} {
		s := cfg.Suites[name]
		if !s.HasSteps() || len(s.Tests) == 0 || !strings.HasPrefix(s.Tests[0], "example/") {
			t.Errorf("suite %s doesn't run the example's tests from example/ by steps", name)
		}
	}
}

func TestTheExampleOnItsOwnPassesCheck(t *testing.T) {
	r := newRepo(t)

	run(t, "check", "-root", r.Dir)
}
