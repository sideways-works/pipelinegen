package report

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

func remoteRefNames(t *testing.T, remote string) []string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", strings.TrimPrefix(remote, "file://"), "for-each-ref", "--format=%(refname)", "refs/pipelinegen/").Output()
	if err != nil {
		t.Fatal(err)
	}

	return strings.Fields(string(out))
}

func TestOnlyTheLastSuitesEndMovesTheRunsRefsTogether(t *testing.T) {
	if storetest.OnBranch {
		t.Skip("a suite's own ref is the refs store's layout")
	}

	ctx := t.Context()
	remote := bareRemote(t)
	r := newReporter(t, remote, newFake(), "")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	for _, suite := range []string{"e2e", "lint"} {
		if err := RecordBegan(ctx, testStore(t, remote), r.ID, suite, ""); err != nil {
			t.Fatal(err)
		}
	}

	group := gitstore.BatchPrefix + "pr/pr-7/" + commit[:12] + "/"
	run := gitstore.RunPrefix + "pr/pr-7/" + commit[:12]

	if _, err := newReporter(t, remote, newFake(), "").Suite(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}

	if got, want := remoteRefNames(t, remote), []string{group + "e2e/" + gitstore.SuiteBatch, group + "lint/" + gitstore.SuiteBatch, run}; !slices.Equal(got, want) {
		t.Fatalf("after the first suite's end: refs %v, want %v", got, want)
	}

	if _, err := newReporter(t, remote, newFake(), "").Suite(ctx, "lint"); err != nil {
		t.Fatal(err)
	}

	if got := remoteRefNames(t, remote); !slices.Equal(got, []string{run}) {
		t.Fatalf("after every suite's end: refs %v", got)
	}

	snap, err := readSnapshot(ctx, testStore(t, remote), r.Config, r.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !allEnded(snap) {
		t.Errorf("the ends moved into the run's ref are not read: %v", snap.ends)
	}
}

func TestAnInterruptedRoundsSuiteRefGoesWithTheSweep(t *testing.T) {
	if storetest.OnBranch {
		t.Skip("a suite's own ref is the refs store's layout")
	}

	ctx := t.Context()
	remote := bareRemote(t)
	r := newReporter(t, remote, newFake(), "")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	for _, suite := range []string{"e2e", "lint"} {
		if err := RecordBegan(ctx, testStore(t, remote), r.ID, suite, ""); err != nil {
			t.Fatal(err)
		}
	}

	// lint never ends
	if _, err := newReporter(t, remote, newFake(), "").Suite(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}

	if got := remoteRefNames(t, remote); len(got) != 3 {
		t.Fatalf("refs %v", got)
	}

	s := testStore(t, remote)
	keep := Keep{Runs: KeepRuns{Age: 7 * 24 * time.Hour}}

	if n, err := Sweep(ctx, s, time.Now().Add(8*24*time.Hour), keep, nil); err != nil || n == 0 {
		t.Fatalf("swept %d: %v", n, err)
	}

	if got := remoteRefNames(t, remote); len(got) != 0 {
		t.Errorf("refs left on the remote: %v", got)
	}
}
