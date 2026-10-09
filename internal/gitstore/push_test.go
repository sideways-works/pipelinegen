package gitstore

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/tries"
)

func fileOf(t *testing.T, g *Store, commit, path string) string {
	t.Helper()

	got, err := g.entriesAt(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}

	id, ok := got[path]
	if !ok {
		t.Fatalf("%s holds no %s: %v", commit, path, got)
	}

	return id
}

func pushWith(t *testing.T, g *Store, ref, tip, id, path string) error {
	t.Helper()

	ctx := t.Context()

	index, err := g.tempIndex()
	if err != nil {
		t.Fatal(err)
	}

	env := []string{"GIT_INDEX_FILE=" + index, noLazyFetch}

	if tip != "" {
		if _, err := g.git(ctx, env, nil, "read-tree", tip); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := g.git(ctx, env, strings.NewReader("100644 "+id+"\t"+path+"\n"), "update-index", "--add", "--index-info"); err != nil {
		t.Fatal(err)
	}

	tree, err := g.git(ctx, env, nil, "write-tree", "--missing-ok")
	if err != nil {
		t.Fatal(err)
	}

	commit, err := g.commitTree(ctx, "test", strings.TrimSpace(tree), tip)
	if err != nil {
		t.Fatal(err)
	}

	return g.pushGroups(ctx, []string{gitPush, porcelain, quiet, atomicPush, g.Remote, "--force-with-lease=" + ref + ":" + tip, commit + ":" + ref})
}

func TestAPushLeavesOutWhatItsParentHoldsAtAnyPath(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")

	ref := BatchPrefix + run + "/e2e/1"

	g, err := groupedStore(t, remote, "1").partial(ctx)
	if err != nil {
		t.Fatal(err)
	}

	tips, err := g.fetch(ctx, []string{ref})
	if err != nil {
		t.Fatal(err)
	}

	id := fileOf(t, g, tips[ref], "e2e/a.spec.ts/=/recording.txt")

	if err := pushWith(t, g, ref, tips[ref], id, "e2e/b.spec.ts/=/recording.txt"); err != nil {
		t.Fatalf("a file the parent holds, at a new path: %v", err)
	}
}

func TestAPushThatNeedsWhatItNeverHadIsADefect(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")

	one, two := BatchPrefix+run+"/e2e/1", BatchPrefix+run+"/e2e/2"

	g, err := groupedStore(t, remote, "2").partial(ctx)
	if err != nil {
		t.Fatal(err)
	}

	tips, err := g.fetch(ctx, []string{one})
	if err != nil {
		t.Fatal(err)
	}

	id := fileOf(t, g, tips[one], "e2e/a.spec.ts/=/recording.txt")

	err = pushWith(t, g, two, "", id, "e2e/a.spec.ts/=/recording.txt")

	switch {
	case !errors.Is(err, ErrMissing):
		t.Fatalf("got %v", err)
	case errors.Is(err, errRefused) || errors.Is(err, errLostRace):
		t.Fatalf("taken for a refusal or a lost race: %v", err)
	case !strings.Contains(err.Error(), id):
		t.Fatalf("names no object: %v", err)
	case kindOf(err) != tries.Defect:
		t.Fatalf("would go again: %v", err)
	}
}

func missingIn(t *testing.T, g *Store, commit string) []string {
	t.Helper()

	out, err := g.git(t.Context(), []string{noLazyFetch}, nil, "rev-list", "--objects", "--missing=print", commit)
	if err != nil {
		t.Fatal(err)
	}

	var missing []string

	for line := range strings.SplitSeq(out, "\n") {
		if id, ok := strings.CutPrefix(line, "?"); ok {
			missing = append(missing, id)
		}
	}

	return missing
}

func TestABatchsEndFetchesOnlyTheFilesItReplaces(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	start := groupedStore(t, remote, "1")
	state(t, start, "a.spec.ts", "running")
	state(t, start, "b.spec.ts", "pending")

	end := groupedStore(t, remote, "1")
	state(t, end, "a.spec.ts", "passed")

	if got := readAll(t, remote)[stateRef("a.spec.ts")]; got != "recording.txt=units of a.spec.ts,state.json=passed" {
		t.Fatalf("reads %q", got)
	}

	g, err := end.partial(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ref := BatchPrefix + run + "/e2e/1"

	tips, err := g.fetch(ctx, []string{ref})
	if err != nil {
		t.Fatal(err)
	}

	startTip := strings.TrimSpace(mustGit(t, g, "rev-parse", "--verify", tips[ref]+"^"))
	missing := missingIn(t, g, startTip)

	for _, path := range []string{"e2e/b.spec.ts/=/state.json", "e2e/b.spec.ts/=/recording.txt"} {
		if !slices.Contains(missing, fileOf(t, g, startTip, path)) {
			t.Errorf("%s was fetched", path)
		}
	}

	if id := fileOf(t, g, startTip, "e2e/a.spec.ts/=/state.json"); slices.Contains(missing, id) {
		t.Errorf("the replaced state.json was never fetched, yet the push landed")
	}
}

func mustGit(t *testing.T, g *Store, args ...string) string {
	t.Helper()

	out, err := g.git(t.Context(), []string{noLazyFetch}, nil, args...)
	if err != nil {
		t.Fatal(err)
	}

	return out
}
