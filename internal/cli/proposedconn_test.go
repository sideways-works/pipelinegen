package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestAForgesChangeIsProposed(t *testing.T) {
	for _, c := range []struct {
		ch   runs.Change
		want bool
	}{
		{runs.Change{Run: "pr", ID: "pr-7", PR: 7}, true},
		{runs.Change{Run: "pr", ID: "git/branch-review%2Fcart", Forge: "git"}, true},
		{runs.Change{Run: "main", ID: "main"}, false},
	} {
		if c.ch.Proposed() != c.want {
			t.Errorf("%+v: proposed %v", c.ch, !c.want)
		}
	}

	cfg := &config.Config{
		Runs:    map[string]config.Run{"main": {Trigger: config.Trigger{Branch: "main"}, Select: config.SelectAll, Record: true}},
		Plugins: []config.Plugin{{Name: "s", Kind: config.KindSBOM}, {Name: "c", Kind: config.KindCorrelator}},
	}
	ch := runs.Change{Run: "main", ID: "git/branch-review%2Fcart", Forge: "git", Commit: goldenCommit}

	if !cfg.Rebuilds("main") {
		t.Fatal("the run doesn't rebuild")
	}

	if rb := rebuildFor(t.Context(), cfg, ch, nil, t.TempDir(), nil, t.TempDir()); rb != nil {
		t.Error("a forge's change rebuilds the graph cold")
	}

	err := planOneStep(t.Context(), cfg, ch, nil, t.TempDir(), t.TempDir(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "plan it in two steps") {
		t.Fatalf("one step: %v", err)
	}
}

func TestAForgesChangeIsSupersededByANewerHead(t *testing.T) {
	root, _, s := runEnv(t)
	gitForgeConfig(t, root, s.Remote)

	first := pushBranch(t, s.Remote, "review/cart")
	cfg := loadConfig(t, root)
	ch := runs.Change{Run: "pr", ID: "git/branch-review%2Fcart", Commit: first, Branch: "review/cart", Forge: "git", Remote: s.Remote}

	var said strings.Builder

	if ok, err := admitted(t.Context(), cfg, ch, nil, &said); err != nil || !ok {
		t.Fatalf("the head: %v %v %s", ok, err, said.String())
	}

	// other content, so the newer head is another commit even within a second.
	second := pushBranch(t, s.Remote, "review/cart", "a newer head\n")

	if ok, err := admitted(t.Context(), cfg, ch, nil, &said); err != nil || ok ||
		!strings.Contains(said.String(), "superseded, not run: review/cart is at "+second[:12]) {
		t.Fatalf("after a push: %v %v %s", ok, err, said.String())
	}
}
