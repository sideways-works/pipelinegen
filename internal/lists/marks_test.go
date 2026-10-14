package lists_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

const (
	unitCreate = "go:api/orders#func:Create"
	unitNotes  = "pg:table:public.notes"
	unitSeed   = "go:api/seed#func:Users"
)

func TestSetupLikeUnitsFollowTheSetupSplit(t *testing.T) {
	recs, _, err := lists.Recordings(map[string][]byte{
		"e2e/a.spec.ts.txt": recording.Recording{Observed: true, Own: []string{unitCreate}, Setup: []string{unitSeed}}.Format(),
		"e2e/b.spec.ts.txt": recording.Recording{Observed: true, Own: []string{unitNotes}}.Format(),
	})
	if err != nil {
		t.Fatal(err)
	}

	on, setup := lists.Bound(recs, true)
	if !slices.Equal(on["e2e/a.spec.ts"], ids(unitCreate)) || !slices.Equal(setup["e2e"], ids(unitSeed)) {
		t.Fatalf("split on: %v %v", on, setup)
	}

	if _, ok := on["e2e/_setup"]; ok {
		t.Fatalf("the setup list is no slice's: %v", on)
	}

	off, setup := lists.Bound(recs, false)
	if !slices.Equal(off["e2e/a.spec.ts"], ids(unitCreate, unitSeed)) || !slices.Equal(off["e2e/b.spec.ts"], ids(unitSeed, unitNotes)) || len(setup) != 0 {
		t.Fatalf("split off: %v %v", off, setup)
	}
}

func TestCantTellKeepsTheListAndMarksTheSlice(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{"e2e/a.spec.ts": ids(unitCreate), "e2e/b.spec.ts": ids(unitNotes)})

	first := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	marked := func(at time.Time) []byte {
		return recording.Recording{CannotTell: []recording.Mark{{Plugin: "sql", Reason: "no statement log", Since: at}}}.Format()
	}

	recs, marks, err := lists.Recordings(map[string][]byte{
		"e2e/a.spec.ts.txt": marked(first),
		"e2e/b.spec.ts.txt": recording.Recording{Observed: true, Own: []string{unitSeed}}.Format(),
	})
	if err != nil {
		t.Fatal(err)
	}

	shrink := map[string]bool{"e2e/a.spec.ts": true, "e2e/b.spec.ts": true}
	if _, err := (lists.Attach{Repo: repo, Run: "nightly", Change: "main", Commit: c[1], To: c[1], Recordings: recs, CannotTell: marks, Shrinkable: shrink, Whole: shrink, Trusted: true}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, src, err := lists.Nearest(ctx, s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	if byName, named, err := lists.Nearest(ctx, s, repo, "HEAD~1"); err != nil || named.Ref != src.Ref || !slices.Equal(byName["e2e/b.spec.ts"], got["e2e/b.spec.ts"]) {
		t.Fatalf("by name: %v %+v %v", byName, named, err)
	}

	if !slices.Equal(got["e2e/a.spec.ts"], ids(unitCreate)) || !slices.Equal(got["e2e/b.spec.ts"], ids(unitSeed)) {
		t.Fatalf("lists %v", got)
	}

	want := lists.Marks{"e2e/a.spec.ts": {{Plugin: "sql", Reason: "no statement log", Since: first}}}
	if len(src.CannotTell) != 1 || !slices.Equal(src.CannotTell["e2e/a.spec.ts"], want["e2e/a.spec.ts"]) {
		t.Fatalf("marks %+v", src.CannotTell)
	}

	if recs, marks, err = lists.Recordings(map[string][]byte{"e2e/a.spec.ts.txt": marked(first.Add(24 * time.Hour))}); err != nil {
		t.Fatal(err)
	}

	if _, err := (lists.Attach{Repo: repo, Run: "main", Change: "main", Commit: c[1], To: c[1], Recordings: recs, CannotTell: marks, Trusted: true}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	if _, src, err = lists.Nearest(ctx, s, repo, c[1]); err != nil || !slices.Equal(src.CannotTell["e2e/a.spec.ts"], want["e2e/a.spec.ts"]) {
		t.Fatalf("marked again: %+v %v", src.CannotTell, err)
	}

	recs, marks, err = lists.Recordings(map[string][]byte{
		"e2e/a.spec.ts.txt": recording.Recording{Observed: true, Own: []string{unitNotes}}.Format(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (lists.Attach{Repo: repo, Run: "main", Change: "main", Commit: c[2], To: c[2], Recordings: recs, CannotTell: marks, Trusted: true,
		Whole: map[string]bool{"e2e/a.spec.ts": true}}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, src, err = lists.Nearest(ctx, s, repo, c[2])
	if err != nil || len(src.CannotTell) != 0 || !slices.Equal(got["e2e/a.spec.ts"], ids(unitCreate, unitNotes)) {
		t.Fatalf("told again: %v %+v %v", got, src.CannotTell, err)
	}
}

func TestAnEmptyFinishedRecordingEmptiesTheList(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{"e2e/a.spec.ts": ids(unitCreate), "e2e/b.spec.ts": ids(unitNotes), "e2e/c.spec.ts": ids(unitSeed)})

	empty := recording.Recording{Observed: true}.Format()

	recs, marks, err := lists.Recordings(map[string][]byte{
		"e2e/a.spec.ts.txt": empty, // passed in a run of everything: its list empties
		"e2e/b.spec.ts.txt": empty, // failed: lists only grow
		"e2e/d.spec.ts.txt": empty, // had none: now an empty one
		"e2e/e.spec.ts.txt": nil,   // no data at all
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (lists.Attach{Repo: repo, Run: "nightly", Change: "main", Commit: c[1], To: c[1], Recordings: recs, CannotTell: marks, Trusted: true,
		Shrinkable: map[string]bool{"e2e/a.spec.ts": true, "e2e/d.spec.ts": true},
		Whole:      map[string]bool{"e2e/a.spec.ts": true, "e2e/b.spec.ts": true, "e2e/c.spec.ts": true, "e2e/d.spec.ts": true}}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, _, err := lists.Nearest(ctx, s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string][]recording.ID{
		"e2e/a.spec.ts": nil, "e2e/b.spec.ts": ids(unitNotes), "e2e/c.spec.ts": ids(unitSeed), "e2e/d.spec.ts": nil,
	} {
		list, ok := got[key]
		if !ok || !slices.Equal(list, want) {
			t.Errorf("%s: %v (present %t), want %v", key, list, ok, want)
		}
	}

	if list, ok := got["e2e/e.spec.ts"]; ok {
		t.Errorf("unrecorded slice has a list: %v", list)
	}
}

func TestAProposedChangesAttachOnlyAdds(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{"e2e/a.spec.ts": ids(unitCreate), "e2e/b.spec.ts": ids(unitNotes)})

	recs, marks, err := lists.Recordings(map[string][]byte{
		"e2e/a.spec.ts.txt":   recording.Recording{Observed: true}.Format(),
		"e2e/b.spec.ts.txt":   recording.Recording{Observed: true, Own: []string{unitSeed}}.Format(),
		"e2e/new.spec.ts.txt": recording.Recording{Observed: true, Own: []string{unitSeed}}.Format(),
	})
	if err != nil {
		t.Fatal(err)
	}

	pr := lists.Attach{Repo: repo, Run: "pr", Change: "pr-5", Commit: c[1], To: c[1], Recordings: recs, CannotTell: marks,
		Shrinkable: map[string]bool{"e2e/a.spec.ts": true, "e2e/b.spec.ts": true, "e2e/new.spec.ts": true}}
	if _, err := pr.Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, _, err := lists.Nearest(ctx, s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got["e2e/a.spec.ts"], ids(unitCreate)) || !slices.Equal(got["e2e/b.spec.ts"], ids(unitSeed, unitNotes)) {
		t.Errorf("lists %v", got)
	}

	if l, ok := got["e2e/new.spec.ts"]; ok {
		t.Errorf("a proposed change made a list: %v", l)
	}
}
