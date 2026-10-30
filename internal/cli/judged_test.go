package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestTheHookAsksOnlyThePinnedPlugin(t *testing.T) {
	dir, cache, ran := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "ran")

	script := "#!/bin/sh\ntouch '" + ran + "'\ncat >/dev/null\n" +
		`echo '{"protocol": 1, "kind": "sbom", "name": "mods", "version": "v1", "candidates": ["**/go.mod", "**/go.sum"]}'` + "\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "mods"), []byte(script)); err != nil {
		t.Fatal(err)
	}

	sum, err := fileSHA256(filepath.Join(dir, "mods"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Plugins: []config.Plugin{{Name: "mods", Kind: config.KindSBOM, Release: &config.Release{Version: "v1", SHA256: sum}}}}
	findings := []check.Finding{
		{Kind: check.KindUncovered, Path: "go.mod", Problem: "no slice depends on this file", Fix: "add it"},
		{Kind: check.KindUncovered, Path: "notes.txt", Problem: "no slice depends on this file", Fix: "add it"},
	}

	got := judgedByPlan(t.Context(), cfg, dir, cache, findings)
	if !got[0].Warning || !strings.Contains(got[0].Problem, "judged by the plan: mods may claim it") || got[1].Warning {
		t.Fatalf("got %+v", got)
	}

	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the pinned plugin wasn't asked")
	}

	if err := os.Remove(filepath.Join(dir, "mods")); err != nil {
		t.Fatal(err)
	}

	if got := judgedByPlan(t.Context(), cfg, dir, cache, findings); !got[0].Warning {
		t.Fatalf("offline: %+v", got[0])
	}

	if err := os.Remove(ran); err != nil {
		t.Fatal(err)
	}

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "mods"), []byte(script+"# tampered\n")); err != nil {
		t.Fatal(err)
	}

	got = judgedByPlan(t.Context(), cfg, dir, t.TempDir(), findings)
	if got[0].Warning || !strings.Contains(got[0].Fix, "mods (its binary is not the one its release pins)") {
		t.Fatalf("tampered: %+v", got[0])
	}

	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the tampered binary ran")
	}

	cfg.Plugins[0].Release, cfg.Plugins[0].Commit = nil, strings.Repeat("0", 40)
	if got := judgedByPlan(t.Context(), cfg, dir, t.TempDir(), findings); got[0].Warning || !strings.Contains(got[0].Fix, "pinned by commit") {
		t.Fatalf("commit pin: %+v", got[0])
	}
}

func TestAReleasePinnedBySumsChecksItsPlatformsLine(t *testing.T) {
	dir, ran := t.TempDir(), filepath.Join(t.TempDir(), "ran")

	script := "#!/bin/sh\ntouch '" + ran + "'\ncat >/dev/null\n" +
		`echo '{"protocol": 1, "kind": "sbom", "name": "mods", "version": "v1", "candidates": ["**/go.mod"]}'` + "\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "mods"), []byte(script)); err != nil {
		t.Fatal(err)
	}

	sum, err := fileSHA256(filepath.Join(dir, "mods"))
	if err != nil {
		t.Fatal(err)
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH
	list := strings.Repeat("1", 64) + "  go-mods-linux-s390x\n" + sum + "  go-mods-" + platform + "\n"
	writeFile(t, filepath.Join(dir, "mods.SHA256SUMS"), list)

	listSum, err := fileSHA256(filepath.Join(dir, "mods.SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Plugins: []config.Plugin{{Name: "mods", Kind: config.KindSBOM, Repository: "example/go-mods", Release: &config.Release{Version: "v1", Sums: listSum}}}}
	findings := []check.Finding{{Kind: check.KindUncovered, Path: "go.mod", Problem: "no slice depends on this file", Fix: "add it"}}

	if got := judgedByPlan(t.Context(), cfg, dir, t.TempDir(), findings); !got[0].Warning {
		t.Fatalf("pinned by sums: %+v", got[0])
	}

	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the plugin wasn't asked")
	}

	for name, change := range map[string]func(){
		"another list": func() { writeFile(t, filepath.Join(dir, "mods.SHA256SUMS"), list+"# another\n") },
		"another binary": func() {
			writeFile(t, filepath.Join(dir, "mods.SHA256SUMS"), list)

			if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "mods"), []byte(script+"# tampered\n")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		if err := os.Remove(ran); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}

		change()

		if got := judgedByPlan(t.Context(), cfg, dir, t.TempDir(), findings); got[0].Warning {
			t.Fatalf("%s: %+v", name, got[0])
		}

		if _, err := os.Stat(ran); err == nil {
			t.Fatalf("%s: it ran", name)
		}
	}
}

func TestACorrelatorThatBindsWithoutLinksFailsTheCheck(t *testing.T) {
	dir := t.TempDir()

	pinned := func(name, caps string) config.Plugin {
		t.Helper()

		script := "#!/bin/sh\ncat >/dev/null\n" +
			`echo '{"protocol": 1, "kind": "correlator", "name": "` + name + `", "version": "v0.1.6", "capabilities": [` + caps + `]}'` + "\n"

		if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, name), []byte(script)); err != nil {
			t.Fatal(err)
		}

		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}

		return config.Plugin{Name: name, Kind: config.KindCorrelator, Release: &config.Release{Version: "v0.1.6", SHA256: sum}}
	}

	cfg := &config.Config{Plugins: []config.Plugin{
		pinned("old-go", `"observe", "bind"`),
		pinned("new-go", `"observe", "bind", "links"`),
		pinned("statements", `"observe", "collect"`),
	}}

	got := unwalkableFindings(t.Context(), cfg, dir, t.TempDir())
	if len(got) != 1 || got[0].Kind != check.KindBadConfig || got[0].Warning ||
		!strings.Contains(got[0].Problem, "old-go v0.1.6 offers no links: this pipelinegen plans by links") {
		t.Fatalf("got %+v", got)
	}
}

func TestACorrelatorReadingWhatObserveCantSeeFailsTheCheck(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`echo '{"protocol": 1, "kind": "correlator", "name": "browser", "version": "v1", "capabilities": ["observe", "links"], "observe_reads": ["build_root", "maps"]}'` + "\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "browser"), []byte(script)); err != nil {
		t.Fatal(err)
	}

	sum, err := fileSHA256(filepath.Join(dir, "browser"))
	if err != nil {
		t.Fatal(err)
	}

	with := func(conf map[string]any) *config.Config {
		return &config.Config{Plugins: []config.Plugin{{Name: "browser", Kind: config.KindCorrelator, Release: &config.Release{Version: "v1", SHA256: sum}, Config: conf}}}
	}

	for _, conf := range []map[string]any{nil, {"build_root": "source/web/dist"}, {"build_root": "coverage/build", "maps": []any{"source/maps", "coverage/maps"}}, {"other": "/anywhere"}} {
		if got := observeReadsFindings(t.Context(), with(conf), dir, t.TempDir()); len(got) != 0 {
			t.Errorf("%v: %+v", conf, got)
		}
	}

	got := observeReadsFindings(t.Context(), with(map[string]any{"build_root": "prepared/tree", "maps": []any{"coverage/maps", "/srv/maps"}}), dir, t.TempDir())
	if len(got) != 2 || got[0].Kind != check.KindBadConfig || got[0].Warning ||
		!strings.HasPrefix(got[0].Problem, `browser config.build_root = "prepared/tree": the step that observes sees only the tree (source/) and the coverage output (coverage/)`) ||
		!strings.Contains(got[1].Problem, `config.maps = "/srv/maps"`) || !strings.Contains(got[0].Fix, "report batch-end, given only source, trigger, batches, result and coverage") {
		t.Fatalf("got %+v", got)
	}
}

func TestAPlanWarnsOfWhatObserveCantSeeAndARecordingRunFails(t *testing.T) {
	cfg := &config.Config{
		Suites: map[string]config.Suite{
			"e2e": {Bind: config.BindRecorded},
			"api": {Bind: config.BindRecorded, Correlators: []string{"go"}},
		},
		Runs: map[string]config.Run{"nightly": {Record: true}, "pr": {}},
		Plugins: []config.Plugin{
			{Name: "browser", Kind: config.KindCorrelator, Config: map[string]any{"build_root": "prepared/tree"}},
			{Name: "go", Kind: config.KindCorrelator},
		},
	}

	p := planning{set: &plugins.Set{Plugins: []*plugins.Loaded{
		{Conf: cfg.Plugins[0], Desc: plugin.Description{Kind: plugin.KindCorrelator, ObserveReads: []string{"build_root"}}},
		{Conf: cfg.Plugins[1], Desc: plugin.Description{Kind: plugin.KindCorrelator}},
	}}}

	lines, records := observeReadsProblems(p, cfg, "nightly", cfg.SuiteNames())
	if !records || len(lines) != 1 || !strings.HasPrefix(lines[0], `browser config.build_root = "prepared/tree": the step that observes sees only the tree`) {
		t.Fatalf("nightly: %t %q", records, lines)
	}

	if lines, records := observeReadsProblems(p, cfg, "pr", cfg.SuiteNames()); records || len(lines) != 1 {
		t.Fatalf("pr: %t %q", records, lines)
	}

	if _, records := observeReadsProblems(p, cfg, "nightly", []string{"api"}); records {
		t.Fatal("api is observed by go alone")
	}

	cfg.Runs["nightly"] = config.Run{Record: true, Suites: map[string]config.RunSuite{"api": {}}}

	plan := &affected.Plan{}
	if err := finishPlan(plan, p, cfg, "nightly", nil, nil); err != nil || len(plan.Warnings) != 1 {
		t.Fatalf("a run that doesn't cover e2e: %v %q", err, plan.Warnings)
	}

	cfg.Runs["nightly"] = config.Run{Record: true, Suites: map[string]config.RunSuite{"e2e": {}}}

	if err := finishPlan(&affected.Plan{}, p, cfg, "nightly", nil, nil); err == nil {
		t.Fatal("a recording run of e2e planned though browser can't observe it")
	}
}
