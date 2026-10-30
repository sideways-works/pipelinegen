package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestAPlanThatRunsEverythingAsksNoCorrelator(t *testing.T) { planOfEverything(t, false) }

func TestAPlanThatRunsEverythingWithEveryFileCoveredAsksNoPlugin(t *testing.T) {
	planOfEverything(t, true)
}

func planOfEverything(t *testing.T, covered bool) {
	t.Helper()
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path
	r := pluginsRepo(t)
	pdir, calls := loggedPluginsDir(t)

	conf := strings.Replace(readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml")), "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
suites:
`, 1)
	if covered {
		conf += "untested:\n  - {paths: [\"**\"], reason: the fixture's other files}\n"
	}

	r.Write("pipelinegen.yaml", conf)
	base := r.Commit("plugins")

	// the fixture runs everything on any change under ci/.
	r.Write("ci/e2e-slice.yml", readFile(t, filepath.Join(r.Dir, "ci/e2e-slice.yml"))+"# changed\n")
	head := r.Commit("a task changed")

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	got, err := planWith(t.Context(), cfg, nil, r.Dir, "pr", base, head, false, "", nil, cache, bd, pdir)
	if err != nil {
		t.Fatal(err)
	}

	if !got.All {
		t.Fatal("plan: not everything")
	}

	c := readCalls(t, calls)

	switch {
	case covered && c != "":
		t.Fatalf("plan asked the plugins:\n%s", c)
	case !covered && (c == "" || slices.ContainsFunc(strings.Split(strings.TrimSpace(c), "\n"), func(l string) bool { return !strings.HasPrefix(l, "texts ") })):
		t.Fatalf("plan asked other than the sbom's claims:\n%s", c)
	}

	if err := os.WriteFile(calls, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-head", head, "-cache", cache, "-bazel-diff", bd, "-plugins", pdir, "-no-lists", "-json")
	if code != 0 {
		t.Fatalf("affected: exit %d: %s", code, stderr)
	}

	var shown affected.Plan
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatal(err)
	}

	if c := readCalls(t, calls); c != "" {
		t.Fatalf("affected asked the plugins:\n%s", c)
	}

	p, done, err := withPlugins(t.Context(), cfg, nil, cache, recording.Lists{}, nil, r.Dir, pdir, "", "")
	if err != nil {
		t.Fatal(err)
	}

	differ, err := differFor(t.Context(), cfg, cache, bd)
	if err != nil {
		t.Fatal(err)
	}

	before, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head,
		Differ: differ, Bazel: bazelFor(cfg, r.Dir, ""), Prepare: p.prepare, Follow: followFor(cfg, "pr"),
	})

	done()

	if err != nil {
		t.Fatal(err)
	}

	if readCalls(t, calls) == "" {
		t.Fatal("binding through the plugins asked them nothing: the test sees no calls")
	}

	if !shown.All || shown.Reason != before.Reason || !strings.Contains(shown.Reason, "everything_on") {
		t.Fatalf("affected: all %v, %q; before %q", shown.All, shown.Reason, before.Reason)
	}

	for _, p := range []*affected.Plan{got, &shown} {
		if !p.All || len(p.Suites) != len(before.Suites) {
			t.Fatalf("%q with %d suites, before %q with %d", p.Reason, len(p.Suites), before.Reason, len(before.Suites))
		}

		for i, sp := range p.Suites {
			if sp.Suite != before.Suites[i].Suite || selectedNames(sp) != selectedNames(before.Suites[i]) || sp.All != before.Suites[i].All {
				t.Errorf("%s selects %q, before %s selected %q", sp.Suite, selectedNames(sp), before.Suites[i].Suite, selectedNames(before.Suites[i]))
			}
		}
	}

	noCalls(t, cache, "a plan of everything")
}

func loggedPluginsDir(t *testing.T) (string, string) {
	t.Helper()

	dir := pluginsDirFor(t)
	log := filepath.Join(t.TempDir(), "calls")

	for _, name := range []string{"texts", "arrows", "binder"} {
		p := filepath.Join(dir, name)
		script := readFile(t, p)
		script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\necho \""+name+" $1\" >> '"+log+"'\n", 1)

		if err := testenv.WriteExecutable(t.Context(), p, []byte(script)); err != nil {
			t.Fatal(err)
		}
	}

	return dir, log
}

func readCalls(t *testing.T, log string) string {
	t.Helper()

	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return ""
	}

	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}
