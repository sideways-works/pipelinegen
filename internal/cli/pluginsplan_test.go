package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testenv"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fakePlugin is an sbom with one unit per text file and a correlator linking a text's unit to each unit
// its lines name ("-> t:b").
func fakePlugin(kind string) map[string]plugin.Handler {
	describe := func(d plugin.Description) plugin.Handler {
		d.Protocol, d.Version = plugin.Protocol, "v0"

		return func([]byte) (any, error) { return d, nil }
	}

	// a tree holding cant-tell/<plugin> is one the plugin can't tell
	// anything about, whatever it is asked.
	read := func(in []byte) (plugin.TreeRequest, error) {
		var req plugin.TreeRequest
		if err := json.Unmarshal(in, &req); err != nil {
			return req, fmt.Errorf("request: %w", err)
		}

		if _, err := os.Stat(filepath.Join(req.Tree, "cant-tell", kind)); err == nil { //nolint:gosec // the test's own tree
			return req, fmt.Errorf("%w: cant-tell/%s", plugin.ErrCantTell, kind)
		}

		return req, nil
	}

	unitOf := func(path string) string { return "t:" + strings.TrimSuffix(filepath.Base(path), ".txt") }

	switch kind {
	case "texts":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindSBOM, Name: "texts", Namespaces: []string{"t"},
				Capabilities: []string{plugin.CallUnits},
				Inputs:       map[string]string{plugin.CallUnits: plugin.PerFile},
			}),
			plugin.CallUnits: func(in []byte) (any, error) {
				req, err := read(in)
				if err != nil {
					return nil, err
				}

				var a plugin.UnitsAnswer

				for _, c := range req.Claimed {
					data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
					if err != nil {
						return nil, fmt.Errorf("units: %w", err)
					}

					group := ""
					if strings.HasPrefix(c.Path, "pkgs/") {
						group = filepath.Dir(c.Path)
					}

					sum := sha256.Sum256(data)
					a.Units = append(a.Units, plugin.Unit{ID: unitOf(c.Path), File: c.Path, Group: group, Digest: hex.EncodeToString(sum[:8])})
				}

				return a, nil
			},
		}
	case "arrows":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "arrows",
				Capabilities: []string{plugin.CallLinks},
				Inputs:       map[string]string{plugin.CallLinks: plugin.PerSet},
				Narrowings: []plugin.Narrowing{
					{Name: plugins.NarrowSetupSplit, OffAt: 3, Note: "setup binds the setup target"},
					{Name: "loose", OffAt: 3, Note: "a loose arrow binds only while the narrowing is off"},
				},
			}),
			plugin.CallLinks: func(in []byte) (any, error) {
				req, err := read(in)
				if err != nil {
					return nil, err
				}

				var a plugin.LinksAnswer

				for _, c := range req.Claimed {
					data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
					if err != nil {
						return nil, fmt.Errorf("links: %w", err)
					}

					set := plugin.Set{Group: c.Path, Inputs: []string{c.Path}}

					for line := range strings.SplitSeq(string(data), "\n") {
						if to, ok := strings.CutPrefix(line, "-> "); ok {
							set.Links = append(set.Links, plugin.Link{From: unitOf(c.Path), To: to, Kind: plugin.LinkUses})
						}

						// a wide link the narrowing "loose" rules out.
						if to, ok := strings.CutPrefix(line, "~> "); ok {
							set.Links = append(set.Links, plugin.Link{From: unitOf(c.Path), To: to, Kind: plugin.LinkUses, Narrowing: "loose"})
						}
					}

					a.Sets = append(a.Sets, set)
				}

				return a, nil
			},
		}
	case "binder":
		return fakeBinder(describe)
	case "sneaky", "tidy":
		return fakeSneaky(describe, kind == "sneaky")
	case "tokens":
		return fakeTokens(describe)
	case "pages":
		return fakePages(describe)
	case "catalogs":
		return fakeCatalogs(describe)
	}

	return nil
}

func pluginsRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := dataRepo(t)
	r.Write("texts/BUILD.bazel", "filegroup(name = \"texts\", srcs = glob([\"*.txt\"]))\n")
	r.Write("texts/a.txt", "a\n-> t:b\n~> t:d\n-> t:n\n")
	r.Write("texts/k.txt", "k\n-> t:m\n-> t:n\n")
	r.Write("texts/m.txt", "m\n")
	r.Write("texts/n.txt", "n\n")
	r.Write("texts/d.txt", "d\n")
	r.Write("texts/b.txt", "b\n")
	r.Write("texts/c.txt", "c\n")
	r.Write("texts/s.txt", "s\n")
	r.Write("specs/BUILD.bazel", `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["fixtures.json"])
filegroup(name = "orders", srcs = ["orders.spec.ts"])
filegroup(name = "fresh", srcs = ["fresh.spec.ts"])
`)
	r.Write("specs/fresh.spec.ts", "test('fresh')\n")
	r.Commit("texts")

	return r
}

func pluginsDirFor(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	for _, name := range []string{"texts", "arrows", "binder", "sneaky", "tidy", "tokens", "pages", "catalogs"} {
		script := "#!/bin/sh\nFAKE_PLUGIN=" + name + " exec '" + exe + "' \"$@\"\n"
		if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, name), []byte(script)); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestRunnerOnlyPlansByLearn(t *testing.T) {
	cfg := &config.Config{Plugins: []config.Plugin{{Name: "ci", Kind: config.KindRunner}}}
	if usesPlugins(cfg) {
		t.Fatal("a runner alone binds through plugins")
	}

	cfg.Plugins = append(cfg.Plugins, config.Plugin{Name: "texts", Kind: config.KindSBOM})
	if !usesPlugins(cfg) {
		t.Fatal("an sbom doesn't bind through plugins")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestAffectedThroughPlugins(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path
	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	cfgFile := filepath.Join(r.Dir, "pipelinegen.yaml")
	conf := readFile(t, cfgFile)
	conf = strings.Replace(conf, "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
suites:
`, 1)
	r.Write("pipelinegen.yaml", conf)
	base := r.Commit("plugins")

	r.Write("texts/c.txt", "c changed\n")
	r.Commit("c")

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-plugins", pdir, "-no-lists", "-json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if !strings.Contains(stdout, `"plugin": "texts"`) || !strings.Contains(stdout, `"plugin": "arrows"`) {
		t.Fatalf("the plan didn't ask the plugins:\n%s", stdout)
	}

	noCalls(t, cache, "affected")

	if _, _, code := run(t, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-plugins", t.TempDir(), "-no-lists", "-json"); code == 0 {
		t.Fatal("affected without its plugins passed")
	}

	noCalls(t, cache, "a failed affected")

	git := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--bare", filepath.Join(git, "owner", "fixture.git")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if _, stderr, code := runIn(t, r.Dir, map[string]string{envGitURL: "file://" + git}, "why", "-root", r.Dir, "-base", base, "-head", "HEAD", "-cache", cache, "-bazel-diff", bd, "-plugins", pdir, "-slice", "e2e/checkout.spec.ts"); code != 0 {
		t.Fatalf("why: exit %d: %s", code, stderr)
	}

	noCalls(t, cache, "why")
}

func TestGraphSuitesBindThroughTheirOwners(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("texts/BUILD.bazel", "filegroup(name = \"texts\", srcs = glob([\"*.txt\"]))\n\nfilegroup(name = \"a\", srcs = [\"a.txt\"])\n")
	r.Write("gspecs/BUILD.bazel", `filegroup(name = "one", srcs = ["one.spec.ts"], data = ["//texts:a"])
filegroup(name = "two", srcs = ["two.spec.ts"])
`)
	r.Write("gspecs/one.spec.ts", "test('one')\n")
	r.Write("gspecs/two.spec.ts", "test('two')\n")
	r.Commit("graph specs")

	pdir := pluginsDirFor(t)
	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Narrowing = 3
	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}
	cfg.Suites["gspecs"] = config.Suite{Tests: []string{"gspecs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindGraph}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a", "t:k"},
		"dspecs/specs/orders.spec.ts":   {"t:b", "t:k"},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	plan := func(base, head string) map[string]string {
		t.Helper()

		set, err := plugins.Load(t.Context(), plugins.Options{
			Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		book := newPlanBook(set)

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
			Follow: followFor(cfg, ""), Narrowed: book.narrowedHook(), Sample: sampleRule(cfg, set),
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("everything: %s", p.Reason)
		}

		out := map[string]string{}
		for _, sp := range p.Suites {
			out[sp.Suite] = selectedNames(sp)
		}

		return out
	}

	for _, c := range []struct {
		file           string
		graph, records string
	}{
		{"texts/c.txt", "", "specs/fresh.spec.ts"},
		{"texts/a.txt", "gspecs/one.spec.ts", "specs/checkout.spec.ts,specs/fresh.spec.ts"},
		{"gspecs/two.spec.ts", "gspecs/two.spec.ts", ""},
	} {
		base := r.Git("rev-parse", "HEAD")

		r.Write(c.file, readFile(t, filepath.Join(r.Dir, c.file))+"changed\n")
		head := r.Commit(c.file)

		got := plan(base, head)
		if got["gspecs"] != c.graph || got["dspecs"] != c.records {
			t.Errorf("%s: graph suite %q, recorded %q; want %q and %q", c.file, got["gspecs"], got["dspecs"], c.graph, c.records)
		}
	}
}

func TestAChangedPluginEntrySelectsWhatItBinds(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	entries := func(arrows, texts string) string {
		conf := readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))
		if i := strings.Index(conf, "\nplugins:\n"); i >= 0 {
			conf = conf[:i+1] + conf[strings.Index(conf, "\nsuites:\n")+1:]
		}

		return strings.Replace(conf, "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: `+texts+`, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: `+arrows+`, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"], config: {depth: 1}}
suites:
`, 1)
	}

	r.Write("pipelinegen.yaml", entries("v1", "v1"))
	r.Write("BUILD.bazel", "filegroup(name = \"config\", srcs = [\"pipelinegen.yaml\"])\n")
	r.Commit("plugins")

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// Only the pins select here, with plugin-entry off; TestNarrowedIsWhatTheOffLevelAdds holds the sample below it.
	cfg.EverythingOn = nil
	cfg.Narrowing = 4
	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a"},
		"dspecs/specs/orders.spec.ts":   {"t:b"},
		"dspecs/specs/fresh.spec.ts":    {},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, arrows, texts string
		want                map[string]string
	}{
		{"the correlator's version", "v2", "v1", map[string]string{"dspecs": "specs/checkout.spec.ts,specs/fresh.spec.ts,specs/orders.spec.ts"}},
		{"the sbom's version", "v2", "v2", map[string]string{"dspecs": "specs/checkout.spec.ts,specs/orders.spec.ts"}},
		{"nothing of the plugins", "v2", "v2", map[string]string{"dspecs": ""}},
	} {
		base := r.Git("rev-parse", "HEAD")

		conf := entries(c.arrows, c.texts)
		if c.name == "nothing of the plugins" {
			conf += "# a comment\n"
		}

		r.Write("pipelinegen.yaml", conf)
		head := r.Commit(c.name)

		book := newPlanBook(set)

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
			Follow: followFor(cfg, ""), Narrowed: book.narrowedHook(), Why: book.whyHook(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("%s: everything: %s", c.name, p.Reason)
		}

		for _, sp := range p.Suites {
			want, ok := c.want[sp.Suite]
			if !ok {
				continue
			}

			if got := selectedNames(sp); got != want {
				t.Errorf("%s: %s selected %q, want %q", c.name, sp.Suite, got, want)
			}

			if c.name == "the correlator's version" && sp.Suite == "dspecs" {
				rs := sp.Reasons["@pipelinegen_deps//slices:specs/orders.spec.ts"]
				if !slices.ContainsFunc(rs, func(r affected.Reason) bool { return r.Kind == affected.KindPlugin && r.What == "arrows" }) {
					t.Errorf("%s: reasons %+v, want plugin arrows", c.name, rs)
				}
			}
		}
	}
}

func TestPluginsClaimsShowsWhatEachPluginClaims(t *testing.T) {
	t.Parallel()

	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	conf := strings.Replace(readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml")), "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/a.txt", "texts/b.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["nowhere/*.txt"]}
suites:
`, 1)
	r.Write("pipelinegen.yaml", conf)
	r.Commit("plugins")

	stdout, stderr, code := runIn(t, r.Dir, nil, "plugins", "claims", "-root", r.Dir, "-plugins", pdir, "-cache", t.TempDir())
	if code != 0 {
		t.Fatalf("plugins claims: %d %s", code, stderr)
	}

	want := "texts\ttexts/a.txt\tincluded\ntexts\ttexts/b.txt\tincluded\narrows\t-\tclaims nothing\n"
	if stdout != want {
		t.Fatalf("claims:\n%s\nwant\n%s", stdout, want)
	}
}

func TestUnitsNamedAsDataBind(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("gspecs/BUILD.bazel", "filegroup(name = \"one\", srcs = [\"one.spec.ts\"])\nfilegroup(name = \"two\", srcs = [\"two.spec.ts\"])\n")
	r.Write("gspecs/one.spec.ts", "// pipelinegen:data t:c\ntest('one')\n")
	r.Write("gspecs/two.spec.ts", "test('two')\n")
	r.Commit("graph specs")

	pdir := pluginsDirFor(t)
	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Narrowing = 3
	cfg.Suites["dspecs"] = config.Suite{
		Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded,
		ExtraDeps: []config.ExtraDeps{{Tests: []string{"specs/orders.spec.ts"}, Deps: []string{"t:c"}}},
	}
	cfg.Suites["gspecs"] = config.Suite{Tests: []string{"gspecs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindGraph}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a"},
		"dspecs/specs/orders.spec.ts":   {"t:b"},
		"dspecs/specs/fresh.spec.ts":    {"t:b"},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	base := r.Git("rev-parse", "HEAD")
	r.Write("texts/c.txt", "c changed\n")
	head := r.Commit("c")

	p, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
		Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), newPlanBook(set), nil),
		Follow: followFor(cfg, ""),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, sp := range p.Suites {
		got[sp.Suite] = selectedNames(sp)
	}

	if got["dspecs"] != "specs/orders.spec.ts" || got["gspecs"] != "gspecs/one.spec.ts" {
		t.Fatalf("selected %v: want orders by its extra_deps and one by its data line", got)
	}
}

func TestAFileAnSBOMClaimsIsCovered(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path
	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	cfgFile := filepath.Join(r.Dir, "pipelinegen.yaml")
	r.Write("pipelinegen.yaml", strings.Replace(readFile(t, cfgFile), "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v0, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt", "deps/*.txt"]}
suites:
`, 1))
	r.Write("deps/lock.txt", "module one v1\n")
	base := r.Commit("a lockfile")

	r.Write("deps/lock.txt", "module one v2\n")
	head := r.Commit("bump")

	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()

	claimed, err := sbomClaimed(t.Context(), cfg, cache, pdir, r.Dir, head)
	if err != nil || claimed["deps/lock.txt"] != "texts" {
		t.Fatalf("claimed %v: %v", claimed, err)
	}

	if other, err := sbomClaimed(t.Context(), cfg, cache, pdir, r.Dir, base); err != nil || other["deps/lock.txt"] != "texts" {
		t.Fatalf("another commit's claims: %v %v", other, err)
	}

	none := func() map[string]string { return nil }
	claims := func() map[string]string { return claimed }

	_, reason, _, err := coverage(t.Context(), cfg, "", r.Dir, head, false, "", none)
	if err != nil || !strings.Contains(reason, "deps/lock.txt") {
		t.Fatalf("unclaimed: %q %v", reason, err)
	}

	if _, reason, _, err = coverage(t.Context(), cfg, "", r.Dir, head, false, "", claims); err != nil || strings.Contains(reason, "deps/lock.txt") {
		t.Fatalf("claimed: %q %v", reason, err)
	}

	coverageOfAPlanOfEverything(t, cfg, r.Dir, head, claims, none)

	pc, err := checkPlanned(t.Context(), cfg, "", r.Dir, "", head, false, "", cache, pdir)
	if err != nil || !pc.all || pc.reason != "nothing to compare with" || pc.claimed["deps/lock.txt"] != "texts" ||
		slices.ContainsFunc(pc.warnings, func(w string) bool { return strings.Contains(w, "deps/lock.txt") }) {
		t.Fatalf("a first run: %+v %v", pc, err)
	}

	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-plugins", pdir, "-no-lists")
	if code != 0 || strings.Contains(stdout, "no Bazel target owns") {
		t.Fatalf("affected: exit %d\n%s\n%s", code, stdout, stderr)
	}

	r.Git("rm", "-q", "deps/lock.txt")
	r.Commit("no lockfile")

	if past, err := sbomClaimed(t.Context(), cfg, cache, pdir, r.Dir, head); err != nil || past["deps/lock.txt"] != "texts" {
		t.Fatalf("the head's claims from a later checkout: %v %v", past, err)
	}

	if leftover, err := filepath.Glob(filepath.Join(cache, "claims-*")); err != nil || len(leftover) > 0 {
		t.Errorf("claims trees left behind: %v %v", leftover, err)
	}

	stdout, stderr, code = run(t, "affected", "-root", r.Dir, "-base", base, "-head", head, "-cache", cache, "-bazel-diff", bd, "-plugins", pdir, "-no-lists")
	if code != 0 || strings.Contains(stdout, "no Bazel target owns") {
		t.Fatalf("affected of a past commit: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestPluginsClaimsSaysWhatARecordingHolds(t *testing.T) {
	set := &plugins.Set{Plugins: []*plugins.Loaded{{Conf: config.Plugin{Name: "react"}}}}
	claimed := map[string][]plugin.Claim{"react": {
		{Path: "web/src/app.tsx", Role: "source", Recorded: true},
		{Path: "web/vite.config.ts", Role: "config"},
		{Path: "web/src/types.ts", Role: "source", Erased: true},
	}}

	var text, js bytes.Buffer

	if err := writeClaims(&text, set, claimed, false); err != nil {
		t.Fatal(err)
	}

	if want := "react\tweb/src/app.tsx\tsource\trecorded\nreact\tweb/src/types.ts\tsource\terased\nreact\tweb/vite.config.ts\tconfig\n"; text.String() != want {
		t.Fatalf("text:\n%s", text.String())
	}

	if err := writeClaims(&js, set, claimed, true); err != nil {
		t.Fatal(err)
	}

	var got []map[string]any
	if err := json.Unmarshal(js.Bytes(), &got); err != nil || len(got) != 3 || got[0]["recorded"] != true || got[0]["plugin"] != "react" ||
		got[1]["erased"] != true || got[2]["recorded"] != nil || got[2]["erased"] != nil {
		t.Fatalf("json %s: %v", js.String(), err)
	}
}

func coverageOfAPlanOfEverything(t *testing.T, cfg *config.Config, root, head string, claims, none func() map[string]string) {
	t.Helper()

	asked := 0
	counted := func() map[string]string {
		asked++

		return claims()
	}

	all, reason, warnings, err := coverage(t.Context(), cfg, "", root, head, true, "nothing to compare with", counted)
	if err != nil || !all || reason != "nothing to compare with" || asked != 1 || slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, "deps/lock.txt") }) {
		t.Fatalf("claimed, no base: %t %q %v %d asks, warnings %q", all, reason, err, asked, warnings)
	}

	all, reason, warnings, err = coverage(t.Context(), cfg, "", root, head, true, "nothing to compare with", none)
	if err != nil || !all || reason != "nothing to compare with" || !slices.ContainsFunc(warnings, func(w string) bool {
		return strings.HasPrefix(w, "files no slice covers: ") && strings.Contains(w, "deps/lock.txt")
	}) {
		t.Fatalf("unclaimed, no base: %t %q %v, warnings %q", all, reason, err, warnings)
	}

	failing := *cfg
	failing.Uncovered = config.UncoveredFail

	if _, _, _, err := coverage(t.Context(), &failing, "", root, head, true, "nothing to compare with", claims); err != nil && strings.Contains(err.Error(), "deps/lock.txt") {
		t.Fatalf("claimed, no base, uncovered: fail: %v", err)
	}

	if _, _, _, err := coverage(t.Context(), &failing, "", root, head, true, "nothing to compare with", none); !errors.Is(err, errUncovered) || !strings.Contains(err.Error(), "deps/lock.txt") {
		t.Fatalf("unclaimed, no base, uncovered: fail: %v", err)
	}
}
