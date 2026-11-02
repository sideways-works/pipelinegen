package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeTokens digests a script's words, so a whitespace change keeps its unit as a syntax-tree digest
// does; "@pragma" stands for a comment a tool reads.
func fakeTokens(describe func(plugin.Description) plugin.Handler) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindSBOM, Name: "tokens", Namespaces: []string{"tok"},
			Capabilities: []string{plugin.CallUnits},
			Inputs:       map[string]string{plugin.CallUnits: plugin.PerFile},
		}),
		plugin.CallUnits: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.UnitsAnswer

			for _, c := range req.Claimed {
				data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
				if err != nil {
					return nil, fmt.Errorf("units: %w", err)
				}

				sum := sha256.Sum256([]byte(strings.Join(strings.Fields(string(data)), " ")))
				a.Units = append(a.Units, plugin.Unit{ID: "tok:" + c.Path + "#init", File: c.Path, Group: c.Path, Digest: hex.EncodeToString(sum[:8])})

				if !strings.Contains(string(data), "@pragma") {
					a.Complete = append(a.Complete, c.Path)
				}
			}

			return a, nil
		},
	}
}

func sharedCauseRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("runner/BUILD.bazel", "filegroup(name = \"runner\", srcs = [\"reporter.js\", \"runner.conf.js\"], visibility = [\"//visibility:public\"])\n")
	r.Write("runner/reporter.js", "const marker = () => process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n")
	r.Write("runner/runner.conf.js", "export default { reporter: './reporter.js' };\n")
	r.Write("app/BUILD.bazel", "filegroup(name = \"app\", srcs = [\"app.js\"])\n")
	r.Write("app/app.js", "export const render = () => 'page';\n")
	r.Write("msgs/BUILD.bazel", "filegroup(name = \"en\", srcs = [\"en.json\"])\nfilegroup(name = \"de\", srcs = [\"de.json\"])\n")
	r.Write("msgs/en.json", "{\"hello\": \"Hello\"}\n")
	r.Write("msgs/de.json", "{\"hello\": \"Hallo\"}\n")
	r.Write("browser/BUILD.bazel", `filegroup(name = "a", srcs = ["a.spec.ts"])
filegroup(name = "b", srcs = ["b.spec.ts"])
filegroup(name = "c", srcs = ["c.spec.ts"])
`)
	r.Write("browser/a.spec.ts", "test('a')\n")
	r.Write("browser/b.spec.ts", "test('b')\n")
	r.Write("browser/c.spec.ts", "test('c')\n")
	r.Write("lint/BUILD.bazel", "filegroup(name = \"format\", srcs = [\"//runner\"], tags = [\"slice-lint\"])\n")
	r.Commit("a browser suite and its runner")

	return r
}

func sharedCauseConfig(t *testing.T, r *testrepo.Repo) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}
	scripts := []string{"runner/*.js", "app/*.js"}

	cfg.Narrowing = 2
	cfg.EverythingOn = nil
	cfg.Suites = map[string]config.Suite{
		"browser": {
			Tests: []string{"browser/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded,
			ExtraDeps: []config.ExtraDeps{
				{Tests: []string{"browser/*.spec.ts"}, Deps: []string{"//runner"}},
				{Tests: []string{"browser/a.spec.ts", "browser/b.spec.ts"}, Deps: []string{"//msgs:en"}},
				{Tests: []string{"browser/c.spec.ts"}, Deps: []string{"//msgs:de"}},
			},
		},
		"lint": {Tag: "slice-lint", RunSlice: "ci/e2e-slice.yml"},
	}
	cfg.Plugins = []config.Plugin{
		{Name: "tokens", Kind: config.KindSBOM, Repository: "o/tokens", Release: release, Include: scripts},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: scripts},
	}

	return cfg
}

func planSharedCause(t *testing.T, r *testrepo.Repo, cfg *config.Config, cache, pdir, base, head string, limit int) (*affected.Plan, int) {
	t.Helper()

	differ := &explainCounter{Differ: bazeldifftest.Tool(t)}
	bz := testrepo.Bazel(t, filepath.Join(cache, "tree"))
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	lists := recording.Lists{
		"browser/browser/a.spec.ts": {"tok:app/app.js#init"},
		"browser/browser/b.spec.ts": {"tok:app/app.js#init"},
		"browser/browser/c.spec.ts": {"tok:app/app.js#init"},
	}

	book := newPlanBook(set)
	walk := walkTreesFor(gitx.Repo{Dir: r.Dir}, base, head, set, narrowingLevel(cfg))

	p, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
		Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, walk),
		Follow: followFor(cfg, ""), Narrowed: narrowedHook(book, walk, set), Why: book.whyHook(), Reasons: book.reasonsHook(),
		ExplainLimit: limit, Sample: sampleRule(cfg, set), Select: walk.selection(), Inputs: inputsHook(book, walk), Unobservable: unobservableHook(walk), Declared: book.declaredHook(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.All {
		t.Fatalf("everything: %s", p.Reason)
	}

	return p, differ.n
}

func suiteOf(p *affected.Plan, name string) affected.SuitePlan {
	for _, sp := range p.Suites {
		if sp.Suite == name {
			return sp
		}
	}

	return affected.SuitePlan{}
}

func reasonsText(sp affected.SuitePlan) string {
	var out []string

	for _, sl := range sp.Selected {
		var rs []string
		for _, r := range sp.Reasons[sl.Label] {
			rs = append(rs, r.Kind+" "+r.What+" "+strings.Join(r.Through, " > "))
		}

		out = append(out, sl.Name+": "+strings.Join(rs, "; "))
	}

	slices.Sort(out)

	return strings.Join(out, "\n")
}

func TestAWhitespaceChangeUnderALabelEverySliceDependsOn(t *testing.T) {
	testrepo.Parallel(t)

	r := sharedCauseRepo(t)
	cfg := sharedCauseConfig(t, r)
	pdir := pluginsDirFor(t)
	cache := t.TempDir()

	base := r.Git("rev-parse", "HEAD")
	r.Write("runner/reporter.js", "const marker = () =>\n  process.env.OUT\n    ? join(process.env.OUT, '.complete')\n    : undefined;\n")
	head := r.Commit("format the reporter")

	p, explained := planSharedCause(t, r, cfg, cache, pdir, base, head, 1)
	t.Logf("plan: %s", summary(p))
	t.Logf("warnings: %q", p.Warnings)

	browser, lint := suiteOf(p, "browser"), suiteOf(p, "lint")
	if len(browser.Selected) != 0 {
		t.Errorf("browser selects %q, want none", selectedNames(browser))
	}

	if len(lint.Selected) != 1 {
		t.Errorf("the format check isn't selected: %+v", lint.Selected)
	}

	if explained > 1 {
		t.Errorf("bazel-diff explained %d times", explained)
	}

	if got := fmt.Sprint(p.Unobservable); got != "[{runner/reporter.js tokens 1}]" {
		t.Errorf("unobservable %s", got)
	}

	if got := summary(p); got != "browser 0 of 3 [unobservable-edits left out 3], lint 1 of 1; 1 file changed without an observable change" {
		t.Errorf("summary %q", got)
	}

	if len(browser.Narrowed) != 1 {
		t.Fatalf("narrowed %+v", browser.Narrowed)
	}

	n := browser.Narrowed[0]
	if n.Narrowing != plugins.NarrowUnobservable || n.OffAt != 4 || strings.Join(n.Dropped, ",") != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" ||
		!strings.Contains(n.Reason, "runner/reporter.js (tokens: its one unit as it was)") || !strings.Contains(n.Reason, "//runner:runner") {
		t.Errorf("narrowed %+v", n)
	}

	var wp whyPlan

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &wp); err != nil {
		t.Fatal(err)
	}

	for _, ws := range wp.Suites {
		if ws.Suite != "browser" {
			continue
		}

		rs := leftOut(&wp, ws, "browser/a.spec.ts", "")
		if len(rs) != 1 || !strings.Contains(rs[0].Text, "the narrowing unobservable-edits left it out: nothing a test can observe changed in runner/reporter.js (tokens: its one unit as it was)") ||
			!strings.Contains(rs[0].Text, "it would have run at narrowing level 4 or above") {
			t.Errorf("why %+v", rs)
		}
	}

	base = head

	r.Write("runner/reporter.js", "const marker = () =>\n  process.env.OUT\n    ? join(process.env.OUT, '.done')\n    : undefined;\n")
	head = r.Commit("the reporter's marker")

	p, explained = planSharedCause(t, r, cfg, cache, pdir, base, head, 1)
	t.Logf("plan: %s", summary(p))

	browser, lint = suiteOf(p, "browser"), suiteOf(p, "lint")
	if got := selectedNames(browser); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
		t.Errorf("a token: browser selects %q", got)
	}

	if len(lint.Selected) != 1 {
		t.Errorf("a token: the format check isn't selected: %+v", lint.Selected)
	}

	if explained != 1 {
		t.Errorf("a token: bazel-diff explained %d times, want once for the label", explained)
	}

	want := "browser/a.spec.ts: target //runner:reporter.js //runner:runner\n" +
		"browser/b.spec.ts: target //runner:reporter.js //runner:runner\n" +
		"browser/c.spec.ts: target //runner:reporter.js //runner:runner"
	if got := reasonsText(browser); got != want {
		t.Errorf("a token: reasons:\n%s\nwant\n%s", got, want)
	}

	if got := summary(p); !strings.HasPrefix(got, "browser 3 of 3 (3: runner/reporter.js changed, through //runner:runner), lint 1 of 1") ||
		strings.Contains(got, "observable") {
		t.Errorf("a token: summary %q", got)
	}

	base = head

	r.Write("msgs/en.json", "{\"hello\": \"Hi\"}\n")
	r.Write("msgs/de.json", "{\"hello\": \"Servus\"}\n")
	head = r.Commit("two catalogs")

	p, explained = planSharedCause(t, r, cfg, cache, pdir, base, head, 25)
	t.Logf("plan: %s", summary(p))

	browser = suiteOf(p, "browser")
	if got := selectedNames(browser); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
		t.Errorf("catalogs: browser selects %q", got)
	}

	if explained != 2 {
		t.Errorf("catalogs: bazel-diff explained %d times, want once per label", explained)
	}

	if got := fmt.Sprint(browser.Causes); got != "[{target //msgs:en.json [//msgs:en] 2}]" {
		t.Errorf("catalogs: causes %s", got)
	}

	if got := summary(p); !strings.HasPrefix(got, "browser 3 of 3 (2: msgs/en.json changed, through //msgs:en; 1 for other reasons)") {
		t.Errorf("catalogs: summary %q", got)
	}

	if got := reasonsText(browser); !strings.Contains(got, "browser/c.spec.ts: target //msgs:de.json //msgs:de") {
		t.Errorf("catalogs: reasons\n%s", got)
	}
}

func TestAnUnobservableChangeStillReachesWhatElseChanged(t *testing.T) {
	testrepo.Parallel(t)

	whitespace := "const marker = () =>\n  process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n"

	t.Run("beside a real change", func(t *testing.T) {
		r := sharedCauseRepo(t)
		cfg := sharedCauseConfig(t, r)

		base := r.Git("rev-parse", "HEAD")
		r.Write("runner/reporter.js", whitespace)
		r.Write("runner/runner.conf.js", "export default { reporter: './reporter.js', retries: 1 };\n")
		head := r.Commit("format the reporter, retry once")

		p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
		t.Logf("plan: %s", summary(p))

		if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
			t.Errorf("browser selects %q", got)
		}

		if len(suiteOf(p, "browser").Narrowed) != 0 {
			t.Errorf("narrowed %+v", suiteOf(p, "browser").Narrowed)
		}

		if got := summary(p); !strings.HasPrefix(got, "browser 3 of 3 (3: runner/runner.conf.js changed, through //runner:runner), lint 1 of 1") {
			t.Errorf("summary %q", got)
		}
	})

	t.Run("off at level 4", func(t *testing.T) {
		r := sharedCauseRepo(t)
		cfg := sharedCauseConfig(t, r)
		cfg.Narrowing = 4

		base := r.Git("rev-parse", "HEAD")
		r.Write("runner/reporter.js", whitespace)
		head := r.Commit("format the reporter")

		p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
		t.Logf("plan: %s", summary(p))

		if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
			t.Errorf("browser selects %q", got)
		}

		if len(p.Unobservable) != 0 {
			t.Errorf("unobservable %v at level 4", p.Unobservable)
		}
	})

	t.Run("a comment a tool reads", func(t *testing.T) {
		r := sharedCauseRepo(t)
		r.Write("runner/reporter.js", "// @pragma env node\nconst marker = () => process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n")
		r.Commit("a pragma")

		cfg := sharedCauseConfig(t, r)

		base := r.Git("rev-parse", "HEAD")
		r.Write("runner/reporter.js", "// @pragma env node\n"+whitespace)
		head := r.Commit("format the reporter")

		p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
		t.Logf("plan: %s", summary(p))

		if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
			t.Errorf("browser selects %q", got)
		}
	})

	t.Run("a test's own data line", func(t *testing.T) {
		r := sharedCauseRepo(t)
		r.Write("browser/c.spec.ts", "// pipelinegen:data //runner\ntest('c')\n")
		r.Commit("c says it reads the runner's files")

		cfg := sharedCauseConfig(t, r)

		base := r.Git("rev-parse", "HEAD")
		r.Write("runner/reporter.js", whitespace)
		head := r.Commit("format the reporter")

		p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
		t.Logf("plan: %s", summary(p))

		if names := selectedNames(suiteOf(p, "browser")); names != "browser/c.spec.ts" {
			t.Errorf("browser selects %q, want the test that declares it reads the files", names)
		}
	})

	t.Run("a reader of its text", func(t *testing.T) {
		r := sharedCauseRepo(t)
		cfg := sharedCauseConfig(t, r)
		browser := cfg.Suites["browser"]
		browser.TextInputs = []config.TextInputs{{Tests: []string{"browser/c.spec.ts"}, Paths: []string{"runner/*.js"}}}
		cfg.Suites["browser"] = browser

		base := r.Git("rev-parse", "HEAD")
		r.Write("runner/reporter.js", whitespace)
		head := r.Commit("format the reporter")

		p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
		t.Logf("plan: %s", summary(p))
		t.Logf("reasons:\n%s", reasonsText(suiteOf(p, "browser")))

		got := suiteOf(p, "browser")
		if names := selectedNames(got); names != "browser/c.spec.ts" {
			t.Errorf("browser selects %q, want the reader of the text", names)
		}

		if !strings.Contains(reasonsText(got), "text-input runner/reporter.js") {
			t.Errorf("the reader's reasons don't say it reads the text:\n%s", reasonsText(got))
		}

		if len(got.Narrowed) != 1 || strings.Join(got.Narrowed[0].Dropped, ",") != "browser/a.spec.ts,browser/b.spec.ts" {
			t.Errorf("narrowed %+v", got.Narrowed)
		}
	})
}

func TestTheRunnersFilesDeclaredApart(t *testing.T) {
	testrepo.Parallel(t)

	r := sharedCauseRepo(t)
	cfg := sharedCauseConfig(t, r)
	browser := cfg.Suites["browser"]
	browser.ExtraDeps[0].Deps = []string{"//runner:runner.conf.js"}
	browser.SampleOn = []string{"runner/reporter.js"}
	browser.Sample = 1
	cfg.Suites["browser"] = browser

	pdir := pluginsDirFor(t)
	cache := t.TempDir()

	base := r.Git("rev-parse", "HEAD")
	r.Write("runner/reporter.js", "const marker = () => process.env.OUT ? join(process.env.OUT, '.done') : undefined;\n")
	head := r.Commit("the reporter's marker")

	p, _ := planSharedCause(t, r, cfg, cache, pdir, base, head, 25)
	t.Logf("reporter: %s", summary(p))

	if got := suiteOf(p, "browser"); len(got.Selected) != 1 || len(got.Reasons) != 0 {
		t.Errorf("reporter: browser selects %q with reasons %v, want a sample of one", selectedNames(got), got.Reasons)
	}

	if got := suiteOf(p, "lint"); len(got.Selected) != 1 {
		t.Errorf("reporter: the format check isn't selected")
	}

	base = head

	r.Write("runner/runner.conf.js", "export default { reporter: './reporter.js', retries: 1 };\n")
	head = r.Commit("the runner's config")

	p, _ = planSharedCause(t, r, cfg, cache, pdir, base, head, 25)
	t.Logf("config: %s", summary(p))

	if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
		t.Errorf("config: browser selects %q", got)
	}
}
