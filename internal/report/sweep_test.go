package report

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func TestSweepRemovesWholeRunsOutOfKeep(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	s := testStore(t, remote)
	gh := newFake()

	plan := []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "untouched": [], "whole_suite": []}]}`)

	pr := ID{Run: "pr", Change: "pr-1", Commit: commit, PR: 1}
	branch := ID{Run: "main", Change: "main", Commit: commit}

	for _, id := range []ID{pr, branch} {
		r := newReporter(t, remote, gh, "")
		r.ID = id

		if err := WritePlan(ctx, s, id, plan); err != nil {
			t.Fatal(err)
		}

		if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.a", Label: "//e2e:a", Running: true}); err != nil {
			t.Fatal(err)
		}

		if _, err := r.Suite(ctx, "e2e"); err != nil {
			t.Fatal(err)
		}
	}

	if err := WriteAlsoTo(ctx, s, "pr", "pr-1", []int{2}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "lists",
		gitstore.Update{Ref: refRoot + "lists/aaa", Files: map[string][]byte{"e2e/e2e.a.txt": []byte("x\n")}},
		gitstore.Update{Ref: refRoot + "lists/bbb", Files: map[string][]byte{"e2e/e2e.a.txt": []byte("y\n")}},
		gitstore.Update{Ref: refRoot + "attached/aaa", Files: map[string][]byte{"pr/pr-1/c.json": []byte("{}\n")}},
		gitstore.Update{Ref: refRoot + "attached/bbb", Files: map[string][]byte{"main/main/c.json": []byte("{}\n")}},
		gitstore.Update{Ref: refRoot + "attached/ccc", Files: map[string][]byte{"main/main/c.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	keep := Keep{
		Runs:   KeepRuns{Age: 5 * 24 * time.Hour},
		PerRun: map[string]KeepRuns{"main": {Last: 1}},
		Lists:  KeepLists{Age: 5 * 24 * time.Hour},
	}

	if n, err := Sweep(ctx, s, time.Now(), keep, nil); err != nil || n != 0 {
		t.Fatalf("young: %d %v", n, err)
	}

	n, err := Sweep(ctx, s, time.Now().Add(10*24*time.Hour), keep, []string{"bbb"})
	if err != nil {
		t.Fatal(err)
	}

	left, err := s.List(ctx, refRoot)
	if err != nil {
		t.Fatal(err)
	}

	if n != 7 || len(left) != 5 {
		t.Fatalf("removed %d, left %v", n, left)
	}

	for _, want := range []string{branch.PlanRef(), branch.StateRef("e2e", "e2e.a"), branch.EndRef("e2e"), refRoot + "lists/bbb", refRoot + "attached/bbb"} {
		if _, ok := left[want]; !ok {
			t.Errorf("%s was swept", want)
		}
	}
}

func TestSweepCountsRunsByTheirPlansTime(t *testing.T) {
	day := 24 * time.Hour
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	runs := map[runKey]*sweptRun{}

	for i, age := range []int{1, 2, 3, 9} {
		k := runKey{"pr", "pr-" + string(rune('a'+i)), "c"}
		runs[k] = &sweptRun{key: k, plan: now.Add(-time.Duration(age) * day), newest: now}
	}

	k := runKey{"nightly", "main", "c"}
	runs[k] = &sweptRun{key: k, newest: now.Add(-30 * day)}

	doomed, kept := sweepRuns(runs, Keep{Runs: KeepRuns{Last: 2}, PerRun: map[string]KeepRuns{"nightly": {Age: 40 * day}}}, now)

	if len(doomed) != 2 || !kept["pr/pr-a"] || !kept["pr/pr-b"] || kept["pr/pr-c"] || !kept["nightly/main"] {
		t.Fatalf("doomed %d, kept %v", len(doomed), kept)
	}

	if doomed, _ := sweepRuns(runs, Keep{}, now); len(doomed) != 0 {
		t.Fatal("swept without a rule")
	}
}

func TestKeepFromConfig(t *testing.T) {
	week := config.Retention{For: "1w", Last: 3}
	cfg := &config.Config{
		Keep: config.Keep{Runs: config.Retention{For: "5d"}, Lists: config.ListsRetention{Retention: config.Retention{Last: 50}}},
		Runs: map[string]config.Run{"nightly": {Keep: &week}, "pr": {}},
	}

	k := KeepFrom(cfg)
	if k.Runs != (KeepRuns{Age: 5 * 24 * time.Hour}) || k.Lists.Last != 50 || k.Lists.Age != 0 || k.Lists.Commits != config.DefaultListsCommits ||
		k.PerRun["nightly"] != (KeepRuns{Last: 3, Age: 7 * 24 * time.Hour}) || len(k.PerRun) != 1 {
		t.Fatalf("%+v", k)
	}
}

func TestAQuietBranchKeepsItsNewestLists(t *testing.T) {
	ctx := t.Context()
	s := testStore(t, bareRemote(t))

	if _, err := s.Write(ctx, "lists",
		gitstore.Update{Ref: refRoot + "lists/old1", Files: map[string][]byte{"e2e/a.txt": []byte("x\n")}},
		gitstore.Update{Ref: refRoot + "attached/old1", Files: map[string][]byte{"main/release-2/c1.json": []byte("{}\n")}},
		gitstore.Update{Ref: refRoot + "lists/pr1", Files: map[string][]byte{"e2e/a.txt": []byte("x\n")}},
		gitstore.Update{Ref: refRoot + "attached/pr1", Files: map[string][]byte{"pr/pr-4/c3.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	s.Clock = func() time.Time { return time.Now().Add(time.Minute) }

	if _, err := s.Write(ctx, "lists",
		gitstore.Update{Ref: refRoot + "lists/new1", Files: map[string][]byte{"e2e/a.txt": []byte("y\n")}},
		gitstore.Update{Ref: refRoot + "attached/new1", Files: map[string][]byte{"main/release-2/c2.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	keep := Keep{Lists: KeepLists{Age: 24 * time.Hour}}

	if _, err := Sweep(ctx, s, time.Now().Add(100*24*time.Hour), keep, nil); err != nil {
		t.Fatal(err)
	}

	left, err := s.List(ctx, refRoot)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{refRoot + "lists/new1", refRoot + "attached/new1"} {
		if _, ok := left[want]; !ok {
			t.Errorf("%s was swept", want)
		}
	}

	for _, gone := range []string{refRoot + "lists/old1", refRoot + "lists/pr1"} {
		if _, ok := left[gone]; ok {
			t.Errorf("%s stayed", gone)
		}
	}
}

func TestSweepTakesTheBatchesOfARunThatNeverEnded(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	id := ID{Run: "pr", Change: "pr-1", Commit: commit, PR: 1}

	if err := WritePlan(ctx, testStore(t, remote), id, []byte(`{"suites": []}`)); err != nil {
		t.Fatal(err)
	}

	for _, batch := range []string{"1", "2"} {
		s := testStore(t, remote)
		s.Batch = batch

		if _, err := s.Write(ctx, "batch", gitstore.Update{Ref: id.StateRef("e2e", "e2e.a"+batch), Any: true,
			Files: map[string][]byte{stateFile: []byte(`{"state": "running"}`)}}); err != nil {
			t.Fatal(err)
		}
	}

	s := testStore(t, remote)
	keep := Keep{Runs: KeepRuns{Age: 7 * 24 * time.Hour}}

	if n, err := Sweep(ctx, s, time.Now(), keep, nil); err != nil || n != 0 {
		t.Fatalf("a young run: %d %v", n, err)
	}

	if n, err := Sweep(ctx, s, time.Now().Add(8*24*time.Hour), keep, nil); err != nil || n != 3 {
		t.Fatalf("swept %d: %v", n, err)
	}

	left, err := s.List(ctx, refRoot)
	if err != nil {
		t.Fatal(err)
	}

	if len(left) != 0 {
		t.Errorf("left %v", left)
	}

	out, err := exec.CommandContext(ctx, "git", "-C", strings.TrimPrefix(remote, "file://"), "for-each-ref", "refs/pipelinegen/").Output()
	if err != nil || len(out) != 0 {
		t.Errorf("refs left on the remote: %s %v", out, err)
	}
}

func TestSweepKeepsTheDefaultBranchsLastCommitsLists(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	s := testStore(t, remote)

	mainline := []string{"m0", "m1", "m2", "m3"}

	var ups []gitstore.Update
	for _, c := range append(slices.Clone(mainline), "f1", "f2") {
		ups = append(ups, gitstore.Update{Ref: refRoot + "lists/" + c, Files: map[string][]byte{"e2e/a.txt": []byte(c + "\n")}})
	}

	if _, err := s.Write(ctx, "lists", ups...); err != nil {
		t.Fatal(err)
	}

	keep := Keep{Lists: KeepLists{Age: 5 * 24 * time.Hour, Commits: 2, Mainline: mainline}}

	if _, err := Sweep(ctx, s, time.Now(), keep, []string{"f1"}); err != nil {
		t.Fatal(err)
	}

	left := func() []string {
		t.Helper()

		refs, err := s.List(ctx, refRoot+"lists/")
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for name := range refs {
			out = append(out, strings.TrimPrefix(name, refRoot+"lists/"))
		}

		slices.Sort(out)

		return out
	}

	if got := left(); !slices.Equal(got, []string{"f1", "f2", "m0", "m1"}) {
		t.Fatalf("young: left %v", got)
	}

	if _, err := Sweep(ctx, s, time.Now().Add(30*24*time.Hour), keep, []string{"f1"}); err != nil {
		t.Fatal(err)
	}

	if got := left(); !slices.Equal(got, []string{"f1", "m0", "m1"}) {
		t.Errorf("old: left %v", got)
	}
}

func TestSweepRemovesALargeRunInChunks(t *testing.T) {
	if storetest.OnBranch {
		t.Skip("a ref per record is the refs store's old layout")
	}

	ctx := t.Context()
	remote := bareRemote(t)
	dir := strings.TrimPrefix(remote, "file://")

	const records = 1000

	var in strings.Builder

	run := "pr/pr-1/" + commit[:12]
	for i := 0; i <= records; i++ {
		ref := refRoot + "runs/" + run + "/e2e/s" + strconv.Itoa(i) + ".spec.ts/="
		if i == records {
			ref = refRoot + "plans/" + run
		}

		body := strconv.Itoa(i)
		fmt.Fprintf(&in, "blob\nmark :%d\ndata %d\n%s\n", 2*i+1, len(body), body)
		fmt.Fprintf(&in, "commit %s\nmark :%d\ncommitter a <a@b> %d +0000\ndata 1\nx\nM 100644 :%d state.json\n\n", ref, 2*i+2, time.Now().AddDate(-1, 0, 0).Unix(), 2*i+1)
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "fast-import", "--quiet")
	cmd.Stdin = strings.NewReader(in.String())

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	hook := "#!/bin/sh\nn=$(wc -l)\n[ $n -le 300 ] || { echo \"too many refs in one push\" >&2; exit 1; }\n"
	if err := testenv.WriteExecutable(ctx, filepath.Join(dir, "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	s := testStore(t, remote)
	s.Retries = 0

	n, err := Sweep(ctx, s, time.Now(), Keep{Runs: KeepRuns{Age: 5 * 24 * time.Hour}}, nil)
	if err != nil || n != records+1 {
		t.Fatalf("removed %d of %d: %v", n, records+1, err)
	}

	out, err := exec.CommandContext(ctx, "git", "-C", dir, "for-each-ref", refRoot).Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("left %s %v", out, err)
	}
}
