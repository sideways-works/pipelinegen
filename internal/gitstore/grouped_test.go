package gitstore

import (
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const run = "pr/pr-7/0123456789ab"

func stateRef(slice string) string { return runsRoot + run + "/e2e/" + slice + "/=" }

func endRef() string { return runsRoot + run + "/_end/e2e" }

func planRef() string { return plansRoot + run }

func groupedStore(t *testing.T, remote, batch string) *Store {
	t.Helper()

	if onBranch {
		t.Skip("the grouped refs are the refs store's layout")
	}

	s := newStore(t, remote)
	s.Batch = batch

	return s
}

func remoteRefs(t *testing.T, remote string) []string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", strings.TrimPrefix(remote, "file://"), "for-each-ref", "--format=%(refname)", "refs/pipelinegen/").Output()
	if err != nil {
		t.Fatal(err)
	}

	return strings.Fields(string(out))
}

func state(t *testing.T, s *Store, slice, body string) {
	t.Helper()

	if _, err := s.Write(t.Context(), "state", Update{Ref: stateRef(slice), Any: true, Files: files("state.json", body, "recording.txt", "units of "+slice)}); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, remote string) map[string]string {
	t.Helper()

	s := newStore(t, remote)

	refs, err := s.List(t.Context(), runsRoot+run+"/", plansRoot+"pr/pr-7/")
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}

	for name, ref := range refs {
		got, err := s.Files(t.Context(), ref.Commit)
		if err != nil {
			t.Fatal(err)
		}

		var parts []string
		for _, f := range slices.Sorted(maps.Keys(got)) {
			parts = append(parts, f+"="+string(got[f]))
		}

		out[name] = strings.Join(parts, ",")
	}

	return out
}

func TestARunsRecordsLiveInFewRefs(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	steps := groupedStore(t, remote, "")

	if _, err := steps.Write(ctx, "plan", Update{Ref: planRef(), Any: true, Files: files("plan.json", "{}")}); err != nil {
		t.Fatal(err)
	}

	one, two := groupedStore(t, remote, "1"), groupedStore(t, remote, "2")
	state(t, one, "a.spec.ts", "passed")
	state(t, one, "b.spec.ts", "failed")
	state(t, two, "c.spec.ts", "passed")

	if _, err := steps.Write(ctx, "end", Update{Ref: endRef(), Any: true, Files: files("end.json", "{}")}); err != nil {
		t.Fatal(err)
	}

	want := []string{BatchPrefix + run + "/e2e/1", BatchPrefix + run + "/e2e/2", RunPrefix + run}
	if got := remoteRefs(t, remote); !slices.Equal(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}

	got := readAll(t, remote)
	for name, body := range map[string]string{
		planRef():             "plan.json={}",
		endRef():              "end.json={}",
		stateRef("a.spec.ts"): "recording.txt=units of a.spec.ts,state.json=passed",
		stateRef("b.spec.ts"): "recording.txt=units of b.spec.ts,state.json=failed",
		stateRef("c.spec.ts"): "recording.txt=units of c.spec.ts,state.json=passed",
	} {
		if got[name] != body {
			t.Errorf("%s: %q, want %q", name, got[name], body)
		}
	}

	if len(got) != 5 {
		t.Errorf("records %v", got)
	}

	ref, ok, err := newStore(t, remote).First(ctx, []string{"refs/pipelinegen/plans/pr/pr-7/ffffffffffff", planRef()})
	if err != nil || !ok || ref.Name != planRef() {
		t.Errorf("first: %+v %v %v", ref, ok, err)
	}
}

func TestCompactionMovesBatchesIntoTheRunsRef(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	for i := range 4 {
		b := groupedStore(t, remote, strconv.Itoa(i))
		state(t, b, fmt.Sprintf("s%d.spec.ts", i), "passed")
	}

	before := readAll(t, remote)

	end := groupedStore(t, remote, "")

	n, err := end.Compact(ctx, runsRoot+run+"/", "e2e")
	if err != nil || n != 4 {
		t.Fatalf("compacted %d: %v", n, err)
	}

	if got := remoteRefs(t, remote); !slices.Equal(got, []string{RunPrefix + run}) {
		t.Fatalf("refs after compaction %v", got)
	}

	if after := readAll(t, remote); !maps2Equal(before, after) {
		t.Errorf("records changed: %v, were %v", after, before)
	}

	cmd := exec.CommandContext(ctx, "git", "rev-list", "--objects", "--all", "--missing=print")
	cmd.Dir = end.Dir + ".groups"
	cmd.Env = append(cmd.Environ(), noLazyFetch)

	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "recording.txt") || strings.Contains(string(out), "state.json") || !strings.Contains(string(out), "\n?") {
		t.Errorf("the compaction downloaded contents:\n%s", out)
	}

	if n, err := end.Compact(ctx, runsRoot+run+"/", "e2e"); err != nil || n != 0 {
		t.Errorf("again: %d %v", n, err)
	}
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func TestWritesDuringCompactionAreKept(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	groupedStore(t, remote, "")

	const batches, each = 4, 5

	var wg sync.WaitGroup

	for b := range batches {
		wg.Go(func() {
			s := groupedStore(t, remote, strconv.Itoa(b))
			for i := range each {
				state(t, s, fmt.Sprintf("b%d-%d.spec.ts", b, i), "passed")
			}
		})
	}

	wg.Go(func() {
		end := groupedStore(t, remote, "")
		for range 3 {
			if _, err := end.Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
				t.Error(err)
			}
		}
	})

	wg.Wait()

	got := readAll(t, remote)
	if len(got) != batches*each {
		t.Fatalf("%d records of %d: %v", len(got), batches*each, got)
	}

	if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
		t.Fatal(err)
	}

	if after := readAll(t, remote); !maps2Equal(got, after) {
		t.Errorf("the last compaction changed records")
	}

	if refs := remoteRefs(t, remote); !slices.Equal(refs, []string{RunPrefix + run}) {
		t.Errorf("refs %v", refs)
	}
}

func TestARerunAfterCompactionWins(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "failed")

	if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
		t.Fatal(err)
	}

	clockStep(t)
	state(t, groupedStore(t, remote, "9"), "a.spec.ts", "passed")

	if got := readAll(t, remote)[stateRef("a.spec.ts")]; !strings.Contains(got, "state.json=passed") {
		t.Fatalf("the rerun reads %q", got)
	}

	if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, remote)[stateRef("a.spec.ts")]; !strings.Contains(got, "state.json=passed") {
		t.Errorf("after compacting again %q", got)
	}
}

// clockStep waits for the next second: records' times are in seconds.
func clockStep(t *testing.T) {
	t.Helper()

	if err := exec.CommandContext(t.Context(), "sleep", "1.1").Run(); err != nil {
		t.Fatal(err)
	}
}

func TestARunPlannedBeforeKeepsItsLayout(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	old := groupedStore(t, remote, "")

	if _, err := old.refsWrite(ctx, "plan", []Update{{Ref: planRef(), Any: true, Files: files("plan.json", "{}")}}); err != nil {
		t.Fatal(err)
	}

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")

	refs := remoteRefs(t, remote)
	if !slices.Contains(refs, stateRef("a.spec.ts")) || slices.ContainsFunc(refs, func(r string) bool { return strings.HasPrefix(r, BatchPrefix) || strings.HasPrefix(r, RunPrefix) }) {
		t.Fatalf("refs %v", refs)
	}

	if got := readAll(t, remote); len(got) != 2 || !strings.Contains(got[stateRef("a.spec.ts")], "passed") {
		t.Errorf("records %v", got)
	}

	if n, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil || n != 0 {
		t.Errorf("compacted a run of the old layout: %d %v", n, err)
	}
}

func TestDeletingARunsRecordsDeletesItsRefs(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")
	state(t, groupedStore(t, remote, ""), "b.spec.ts", "passed")

	s := newStore(t, remote)

	refs, err := s.List(ctx, runsRoot)
	if err != nil {
		t.Fatal(err)
	}

	ups := make([]Update, 0, len(refs))
	for _, r := range refs {
		ups = append(ups, Update{Ref: r.Name, Old: r.Commit, Delete: true})
	}

	if _, err := s.Write(ctx, "sweep", ups...); err != nil {
		t.Fatal(err)
	}

	if got := remoteRefs(t, remote); len(got) != 0 {
		t.Errorf("left %v", got)
	}
}

func TestAGuardedDeleteOfAChangedRecordIsStale(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	w := groupedStore(t, remote, "")

	state(t, w, "a.spec.ts", "running")

	s := newStore(t, remote)

	refs, err := s.List(ctx, runsRoot)
	if err != nil {
		t.Fatal(err)
	}

	state(t, w, "a.spec.ts", "passed")

	ref := refs[stateRef("a.spec.ts")]
	if _, err := s.Write(ctx, "sweep", Update{Ref: ref.Name, Old: ref.Commit, Delete: true}); err == nil || !strings.Contains(err.Error(), ErrStale.Error()) {
		t.Errorf("deleted a changed record: %v", err)
	}

	if got := readAll(t, remote); !strings.Contains(got[stateRef("a.spec.ts")], "passed") {
		t.Errorf("records %v", got)
	}
}

func TestWritersOnTwoMachinesReplaceEachOthersRecords(t *testing.T) {
	for _, batch := range []string{"1", ""} {
		for _, seeded := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%q seeded=%v", batch, seeded), func(t *testing.T) {
				ctx := t.Context()
				remote := bare(t)

				open := func() *Store {
					s := groupedStore(t, remote, batch)
					if seeded {
						s.Dir, s.Seed = "", filepath.Join(t.TempDir(), "seed")
					}

					return s
				}

				x, y := open(), open()

				writes := []struct {
					by    *Store
					files map[string][]byte
				}{
					{x, files("state.json", "passed", "recording.txt", "units of x")},
					{y, files("state.json", "failed", "failures.json", "[1]")},
					{x, files("state.json", "errored", "recording.txt", "other units", "extra/log.txt", "tail")},
					{y, files("state.json", "passed")},
				}

				for i, w := range writes {
					if _, err := w.by.Write(ctx, "slice", Update{Ref: stateRef("a.spec.ts"), Any: true, Files: w.files}); err != nil {
						t.Fatalf("write %d: %v", i, err)
					}

					var parts []string
					for _, f := range slices.Sorted(maps.Keys(w.files)) {
						parts = append(parts, f+"="+string(w.files[f]))
					}

					if got, want := readAll(t, remote)[stateRef("a.spec.ts")], strings.Join(parts, ","); got != want {
						t.Fatalf("after write %d: %q, want %q", i, got, want)
					}
				}

				if _, err := open().Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
					t.Fatal(err)
				}

				if _, err := x.Write(ctx, "slice", Update{Ref: stateRef("a.spec.ts"), Any: true, Files: files("state.json", "running")}); err != nil {
					t.Fatalf("after compaction: %v", err)
				}

				if got := readAll(t, remote)[stateRef("a.spec.ts")]; got != "state.json=running" {
					t.Errorf("after compaction %q", got)
				}
			})
		}
	}
}
