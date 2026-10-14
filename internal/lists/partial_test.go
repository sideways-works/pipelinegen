package lists_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
)

const (
	passes  = "api/passes_test.go"
	fails   = "api/fails_test.go"
	marked  = "api/marked_test.go"
	fresh   = "api/fresh_test.go"
	setupOf = "api/" + recording.SetupList

	unitOld   = "api/orders#func:Old"
	unitNew   = "api/orders#func:New"
	unitLater = "api/orders#func:Later"
	seedOld   = "api/seed#func:Old"
	seedNew   = "api/seed#func:New"
)

func liveRecording(t *testing.T, s *gitstore.Store, id report.ID, key, state string, rec recording.Recording) {
	t.Helper()

	suite, slice, _ := strings.Cut(key, "/")
	if _, err := s.Write(t.Context(), "run", gitstore.Update{Ref: id.StateRef(suite, slice), Files: map[string][]byte{
		"state.json": []byte(`{"state": "` + state + `"}`), "recording.txt": rec.Format(),
	}}); err != nil {
		t.Fatal(err)
	}
}

func replacedAt(t *testing.T, s *gitstore.Store, repo, commit string) map[string]string {
	t.Helper()

	_, src, err := lists.Nearest(t.Context(), s, repo, commit)
	if err != nil || src.Back != 0 {
		t.Fatalf("the lists of %s: %+v %v", commit[:7], src, err)
	}

	return src.Replaced
}

func TestARoundThatFailedKeepsWhatPassed(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	first := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	all := map[string]bool{passes: true, fails: true}

	if _, err := (lists.Attach{
		Repo: repo, Run: "full", Change: "main", Commit: c[0], To: c[0], Trusted: true,
		Recordings: recording.Lists{passes: ids(unitOld), fails: ids(unitOld), setupOf: ids(seedOld)},
		CannotTell: lists.Marks{marked: {{Plugin: "react", Reason: "no build holds it", Since: first}}},
		Shrinkable: all, Whole: all, SuitesPassed: map[string]bool{"api": true},
	}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	id := report.ID{Run: "full", Change: "main", Commit: c[1]}
	liveRecording(t, s, id, fails, "failed", recording.Recording{Observed: true, Own: []string{unitLater}})

	recs := recording.Lists{passes: ids(unitNew), fails: ids(unitLater), fresh: ids(unitNew), setupOf: ids(seedNew)}
	passed := map[string]bool{passes: true, fresh: true}

	got, err := (lists.Attach{
		Repo: repo, Run: "full", Change: "main", Commit: c[1], To: c[1], Trusted: true,
		Recordings: recs, Shrinkable: passed, Whole: passed, SuitesPassed: map[string]bool{},
	}).Do(ctx, s)
	if err != nil || got.Again {
		t.Fatalf("attach: %+v %v", got, err)
	}

	expect(t, s, repo, c[1], recording.Lists{
		passes: ids(unitNew), fails: ids(unitLater, unitOld), fresh: ids(unitNew), setupOf: ids(seedNew, seedOld),
	})

	want := map[string]string{passes: c[1], fresh: c[1], fails: c[0], setupOf: c[0]}
	if at := replacedAt(t, s, repo, c[1]); !sameMap(at, want) {
		t.Fatalf("replaced at %v, want %v", at, want)
	}

	l, src, err := lists.For(ctx, s, repo, c[2])
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(l[fails], ids(unitLater, unitOld)) || !slices.Equal(l[passes], ids(unitNew)) {
		t.Fatalf("plan's lists %v", l)
	}

	if ms := src.CannotTell[marked]; len(ms) != 1 || ms[0].Reason != "no build holds it" || !ms[0].Since.Equal(first) {
		t.Fatalf("the marked slice that failed: %+v", src.CannotTell)
	}
}

func sameMap(a, b map[string]string) bool {
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

func TestARerunAttachesWhatPassedSince(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{passes: ids(unitOld), fails: ids(unitOld), marked: ids(unitOld), setupOf: ids(seedOld)})

	since := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	cannot := lists.Marks{marked: {{Plugin: "react", Reason: "no build holds it", Since: since}}}

	round := func(passed map[string]bool, suites map[string]bool) *lists.Attached {
		t.Helper()

		got, err := (lists.Attach{
			Repo: repo, Run: "full", Change: "main", Commit: c[1], To: c[1], Trusted: true,
			Recordings: recording.Lists{passes: ids(unitNew), fails: ids(unitNew, unitLater), setupOf: ids(seedNew)},
			CannotTell: cannot, Shrinkable: passed, Whole: passed, SuitesPassed: suites,
		}).Do(ctx, s)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	round(map[string]bool{passes: true}, map[string]bool{})

	if _, err := (lists.Attach{
		Repo: repo, Run: "main", Change: "main", Commit: c[1], To: c[1], Trusted: true,
		Recordings: recording.Lists{passes: ids(unitLater), marked: ids(unitNew)},
		Whole:      map[string]bool{passes: true, marked: true},
	}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got := round(map[string]bool{passes: true, fails: true}, map[string]bool{"api": true})
	if got.Again || got.Grown != 2 {
		t.Fatalf("the rerun's attach: %+v", got)
	}

	expect(t, s, repo, c[1], recording.Lists{
		passes: ids(unitLater, unitNew), fails: ids(unitLater, unitNew), marked: ids(unitNew, unitOld), setupOf: ids(seedNew),
	})

	_, src, err := lists.Nearest(ctx, s, repo, c[1])
	if err != nil || len(src.CannotTell) != 1 || len(src.CannotTell[marked]) != 1 {
		t.Fatalf("marks after the rerun: %+v %v", src.CannotTell, err)
	}

	if src.Replaced[fails] != c[1] || src.Replaced[setupOf] != c[1] {
		t.Fatalf("replaced %v", src.Replaced)
	}

	if got := round(map[string]bool{passes: true, fails: true}, map[string]bool{"api": true}); !got.Again {
		t.Fatalf("attached a third time: %+v", got)
	}

	if att, err := lists.Attachments(ctx, s, c[1]); err != nil || len(att) != 2 {
		t.Fatalf("attachments %v %v", att, err)
	}
}

func TestTheNewestCouldntTellGivesTheReason(t *testing.T) {
	repo, c := history(t, 4)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{marked: ids(unitOld)})

	rounds := []struct {
		reason string
		at     time.Time
	}{
		{"no build in the tree holds it", time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)},
		{"is not the script the browser ran", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)},
		{"the recording kept no copy of it", time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC)},
	}

	for i, r := range rounds {
		mark := recording.Mark{Plugin: "react", Reason: r.reason, Since: r.at}
		id := report.ID{Run: "full", Change: "main", Commit: c[i]}
		liveRecording(t, s, id, marked, "passed", recording.Recording{Observed: true, CannotTell: []recording.Mark{mark}})

		if _, err := (lists.Attach{
			Repo: repo, Run: "full", Change: "main", Commit: c[i], To: c[i], Trusted: true,
			CannotTell: lists.Marks{marked: {mark}}, Shrinkable: map[string]bool{marked: true}, Whole: map[string]bool{marked: true},
		}).Do(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	check := func(at, reason string, since time.Time) {
		t.Helper()

		_, src, err := lists.For(ctx, s, repo, at)
		if err != nil {
			t.Fatal(err)
		}

		if ms := src.CannotTell[marked]; len(ms) != 1 || ms[0].Reason != reason || !ms[0].Since.Equal(since) {
			t.Fatalf("at %s: %+v, want %q since %s", at[:7], ms, reason, since)
		}
	}

	_, src, err := lists.Nearest(ctx, s, repo, c[2])
	if err != nil || len(src.CannotTell[marked]) != 1 || src.CannotTell[marked][0].Reason != rounds[2].reason {
		t.Fatalf("the lists' own mark: %+v %v", src.CannotTell, err)
	}

	check(c[2], rounds[2].reason, rounds[0].at)
	check(c[3], rounds[2].reason, rounds[0].at)

	early := recording.Mark{Plugin: "react", Reason: "an older attempt", Since: rounds[2].at.Add(time.Hour)}
	liveRecording(t, s, report.ID{Run: "main", Change: "main", Commit: c[2]}, marked, "failed",
		recording.Recording{Observed: true, CannotTell: []recording.Mark{early}})
	check(c[3], rounds[2].reason, rounds[0].at)

	later := recording.Mark{Plugin: "react", Reason: "no coverage of the page", Since: time.Now().Add(time.Hour)}
	liveRecording(t, s, report.ID{Run: "main", Change: "main", Commit: c[3]}, marked, "failed",
		recording.Recording{Observed: true, CannotTell: []recording.Mark{later}})
	check(c[3], later.Reason, rounds[0].at)

	after := recording.Mark{Plugin: "react", Reason: "the newest attempt", Since: time.Now().Add(2 * time.Hour)}
	liveRecording(t, s, report.ID{Run: "branch", Change: "main", Commit: c[2]}, marked, "failed",
		recording.Recording{Observed: true, CannotTell: []recording.Mark{after}})
	check(c[3], after.Reason, rounds[0].at)
}

func TestRoundsAttachedOutOfOrder(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{passes: ids(unitOld), fails: ids(unitOld)})

	newer := map[string]bool{passes: true}
	if _, err := (lists.Attach{
		Repo: repo, Run: "full", Change: "main", Commit: c[2], To: c[2], Trusted: true,
		Recordings: recording.Lists{passes: ids(unitLater)}, Shrinkable: newer, Whole: newer,
	}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	older := map[string]bool{passes: true, fails: true}
	liveRecording(t, s, report.ID{Run: "full", Change: "main", Commit: c[1]}, fails, "passed",
		recording.Recording{Observed: true, Own: []string{unitNew}})

	if _, err := (lists.Attach{
		Repo: repo, Run: "full", Change: "main", Commit: c[1], To: c[1], Trusted: true,
		Recordings: recording.Lists{passes: ids(unitNew), fails: ids(unitNew)}, Shrinkable: older, Whole: older,
	}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	expect(t, s, repo, c[1], recording.Lists{passes: ids(unitNew), fails: ids(unitNew)})
	expect(t, s, repo, c[2], recording.Lists{passes: ids(unitLater), fails: ids(unitOld)})

	l, _, err := lists.For(ctx, s, repo, c[2])
	if err != nil || !slices.Equal(l[fails], ids(unitNew, unitOld)) || !slices.Equal(l[passes], ids(unitLater)) {
		t.Fatalf("a plan of the newer commit: %v %v", l, err)
	}
}

func TestPartialListsOutliveTheSweptOnes(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{passes: ids(unitOld), fails: ids(unitOld)})

	passed := map[string]bool{passes: true}
	if _, err := (lists.Attach{
		Repo: repo, Run: "full", Change: "main", Commit: c[1], To: c[1], Trusted: true,
		Recordings: recording.Lists{passes: ids(unitNew)}, Shrinkable: passed, Whole: passed,
	}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "sweep", gitstore.Update{Ref: lists.Ref(c[0]), Any: true, Delete: true}); err != nil {
		t.Fatal(err)
	}

	l, src, err := lists.For(ctx, s, repo, c[2])
	if err != nil || src.From != c[1] || !slices.Equal(l[fails], ids(unitOld)) || !slices.Equal(l[passes], ids(unitNew)) {
		t.Fatalf("after the sweep: %v %+v %v", l, src, err)
	}
}

func TestARerunOfARunThatAddsAddsWhatPassedSince(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{fails: ids(unitOld)})

	attach := func(recs recording.Lists, passed map[string]bool, cannot lists.Marks) *lists.Attached {
		t.Helper()

		got, err := (lists.Attach{
			Repo: repo, Run: "main", Change: "main", Commit: c[1], To: c[1], Trusted: true,
			Recordings: recs, CannotTell: cannot, Whole: passed,
		}).Do(ctx, s)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	attach(recording.Lists{fails: ids(unitLater), fresh: ids(unitNew)}, map[string]bool{}, nil)
	expect(t, s, repo, c[1], recording.Lists{fails: ids(unitLater, unitOld)})

	mark := lists.Marks{marked: {{Plugin: "react", Reason: "no build holds it", Since: time.Now().UTC().Truncate(time.Second)}}}
	recs := recording.Lists{fails: ids(unitLater, unitNew), fresh: ids(unitNew)}
	passed := map[string]bool{fails: true, fresh: true, marked: true}

	if got := attach(recs, passed, mark); got.Again {
		t.Fatalf("the rerun's attach wrote nothing: %+v", got)
	}

	expect(t, s, repo, c[1], recording.Lists{fails: ids(unitLater, unitNew, unitOld), fresh: ids(unitNew)})

	_, src, err := lists.Nearest(ctx, s, repo, c[1])
	if err != nil || len(src.CannotTell[marked]) != 1 {
		t.Fatalf("the slice that passed only now and couldn't be told: %+v %v", src.CannotTell, err)
	}

	if got := attach(recs, passed, mark); !got.Again {
		t.Fatalf("attached a third time: %+v", got)
	}
}
