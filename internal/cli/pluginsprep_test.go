package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testenv"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

type recordingWriter struct {
	tree   *deps.Tree
	slices []deps.Slice
}

func (w *recordingWriter) Write(_ context.Context, _ string, t *deps.Tree, s []deps.Slice) (string, error) {
	w.tree, w.slices = t, s

	return "key", nil
}

const quietCorrelator = `#!/bin/sh
cat >/dev/null
case "$1" in
describe) echo '{"protocol": 1, "kind": "correlator", "name": "quiet", "version": "v0", "capabilities": []}' ;;
*) exit 1 ;;
esac
`

func TestPluginsPrepareHandsTheWriterEverySlice(t *testing.T) {
	testrepo.Parallel(t)

	r := dataRepo(t)
	bz := testrepo.Bazel(t, r.Dir)

	dir := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "quiet"), []byte(quietCorrelator)); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Narrowing: config.DefaultNarrowing,
		Suites: map[string]config.Suite{
			"specs": {Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", Bind: config.BindRecorded},
		},
		Plugins: []config.Plugin{{Name: "quiet", Kind: config.KindCorrelator, Repository: "o/q", Commit: strings.Repeat("0", 40)}},
	}

	set, err := plugins.Load(t.Context(), plugins.Options{Config: cfg, Dir: dir, Cache: t.TempDir(), Answers: &plugincache.Cache{Dir: t.TempDir()}, Work: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	w := &recordingWriter{}
	lists := recording.Lists{"specs/specs/checkout.spec.ts": {}}

	prepare := pluginsPrepare(cfg, lists, nil, func(string) *bazel.Client { return bz }, set, w, nil, nil)

	tests := map[string][]bazel.Slice{"specs": {
		{Name: "specs/checkout.spec.ts", Sources: []string{"specs/checkout.spec.ts"}},
		{Name: "specs/orders.spec.ts", Sources: []string{"specs/orders.spec.ts"}},
	}}

	key, err := prepare(t.Context(), r.Dir, "c", tests)
	if err != nil || key != "key" {
		t.Fatalf("%q %v", key, err)
	}

	// Both slices in both widths and, while plugin-entry is on, the suite's entry target in both.
	if len(w.slices) != 6 {
		t.Fatalf("slices %+v", w.slices)
	}

	for _, d := range w.slices {
		switch d.Name {
		case affected.EntryTarget("specs"):
			if !slices.Equal(d.Units, []string{plugins.PinID("quiet")}) {
				t.Errorf("the entry target %s: %+v", d.Width, d)
			}
		case "specs/checkout.spec.ts":
			if d.Everything || !slices.Equal(d.Data, []string{"//specs:fixtures.json", "//web"}) {
				t.Errorf("checkout %s: %+v", d.Width, d)
			}
		case "specs/orders.spec.ts":
			if !d.Everything {
				t.Errorf("orders without a list binds everything: %+v", d)
			}
		}
	}
}

func TestACorrelatorThatCouldNotTell(t *testing.T) {
	testrepo.Parallel(t)

	r := dataRepo(t)
	bz := testrepo.Bazel(t, r.Dir)

	dir := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "quiet"), []byte(quietCorrelator)); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Narrowing: config.DefaultNarrowing,
		Suites:    map[string]config.Suite{"specs": {Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", Bind: config.BindRecorded}},
		Plugins:   []config.Plugin{{Name: "quiet", Kind: config.KindCorrelator, Repository: "o/q", Commit: strings.Repeat("0", 40)}},
	}

	const key = "specs/specs/checkout.spec.ts"

	marked := lists.Marks{key: {{Plugin: "quiet", Reason: "no coverage"}}}

	for _, c := range []struct {
		name       string
		lists      recording.Lists
		marks      lists.Marks
		everything bool
	}{
		{"marked, with a list", recording.Lists{key: {"q:x"}}, marked, true},
		{"marked on its first recording", recording.Lists{}, marked, true},
		{"an empty list", recording.Lists{key: {}}, nil, false},
	} {
		set, err := plugins.Load(t.Context(), plugins.Options{Config: cfg, Dir: dir, Cache: t.TempDir(), Answers: &plugincache.Cache{Dir: t.TempDir()}, Work: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}

		w := &recordingWriter{}
		prepare := pluginsPrepare(cfg, c.lists, c.marks, func(string) *bazel.Client { return bz }, set, w, nil, nil)

		tests := map[string][]bazel.Slice{"specs": {{Name: "specs/checkout.spec.ts", Sources: []string{"specs/checkout.spec.ts"}}}}
		if _, err := prepare(t.Context(), r.Dir, "c", tests); err != nil {
			t.Fatal(err)
		}

		for _, d := range w.slices {
			if d.Everything != c.everything {
				t.Errorf("%s: %s %s binds everything: %v, want %v", c.name, d.Name, d.Width, d.Everything, c.everything)
			}
		}

		ws := set.Report().Warnings

		if c.marks == nil {
			if len(ws) != 0 {
				t.Errorf("%s: warnings %v", c.name, ws)
			}

			continue
		}

		if len(ws) != 1 || !strings.Contains(ws[0], "quiet couldn't tell: no coverage") {
			t.Errorf("%s: warnings %v", c.name, ws)
		}
	}
}

func TestAffectedPrintsPlanWarnings(t *testing.T) {
	var b strings.Builder

	planWarnings(&b, &affected.Plan{Warnings: []string{"a snapshot hashed again"}, Plugins: &plugins.Report{Warnings: []string{"e2e/a: quiet couldn't tell"}}})

	if got := b.String(); got != "pipelinegen affected: a snapshot hashed again\npipelinegen affected: e2e/a: quiet couldn't tell\n" {
		t.Fatalf("stderr %q", got)
	}
}

func TestTheTopLevelFollowsDependents(t *testing.T) {
	cfg := &config.Config{Narrowing: 3, Follow: config.FollowExecuted, Suites: map[string]config.Suite{"s": {}}}
	if got := followFor(cfg, "")("s"); got != config.FollowExecuted {
		t.Errorf("level 3: %s", got)
	}

	cfg.Suites["s"] = config.Suite{Narrowing: config.MaxNarrowing}
	if got := followFor(cfg, "")("s"); got != config.FollowDependents {
		t.Errorf("level 5: %s", got)
	}
}
