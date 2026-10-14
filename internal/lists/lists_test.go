package lists_test

import (
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func history(t *testing.T, n int) (string, []string) {
	t.Helper()

	dir := t.TempDir()
	git(t, dir, "init", "--quiet", "-b", "main")

	var commits []string

	for i := range n {
		git(t, dir, "commit", "--quiet", "--allow-empty", "-m", "c"+string(rune('a'+i)))
		commits = append(commits, git(t, dir, "rev-parse", "HEAD"))
	}

	return dir, commits
}

func remote(t *testing.T) *gitstore.Store {
	t.Helper()

	s := storetest.Open(t, storetest.Bare(t))

	return s
}

func ids(s ...string) []recording.ID {
	out := make([]recording.ID, 0, len(s))
	for _, x := range s {
		out = append(out, recording.ID(x))
	}

	return out
}

const (
	create = "api/orders#func:Create"
	total  = "api/orders#method:Order.Total"
	refund = "api/orders#func:Refund"
)

func putLists(t *testing.T, s *gitstore.Store, commit string, l recording.Lists) {
	t.Helper()

	if _, err := s.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(commit), Files: lists.Encode(l)}); err != nil {
		t.Fatal(err)
	}
}

func TestNearestTakesTheOwnRefElseTheNearestAncestor(t *testing.T) {
	repo, c := history(t, 4)
	s := remote(t)

	got, src, err := lists.Nearest(t.Context(), s, repo, c[3])
	if err != nil || len(got) != 0 || src.Ref != "" {
		t.Fatalf("no lists anywhere: %v %v %+v", got, err, src)
	}

	putLists(t, s, c[1], recording.Lists{"api/orders_test.go": ids(create)})

	got, src, err = lists.Nearest(t.Context(), s, repo, c[3])
	if err != nil {
		t.Fatal(err)
	}

	if src.From != c[1] || src.Back != 2 || src.Ref != lists.Ref(c[1]) || !slices.Equal(got["api/orders_test.go"], ids(create)) {
		t.Fatalf("ancestor's: %+v %v", src, got)
	}

	putLists(t, s, c[3], recording.Lists{"api/orders_test.go": ids(total)})

	got, src, err = lists.Nearest(t.Context(), s, repo, c[3])
	if err != nil || src.Back != 0 || !slices.Equal(got["api/orders_test.go"], ids(total)) {
		t.Fatalf("own: %v %+v %v", err, src, got)
	}
}

func TestForUnionsTheRecordingsOfLiveRuns(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)

	putLists(t, s, c[0], recording.Lists{"api/orders_test.go": ids(create)})

	id := report.ID{Run: "pr", Change: "pr-7", Commit: c[1]}
	rec := func(suite, slice string, body string) gitstore.Update {
		return gitstore.Update{Ref: id.StateRef(suite, slice), Files: map[string][]byte{
			"state.json": []byte(`{"state": "passed"}`), "recording.txt": []byte(body),
		}}
	}

	if _, err := s.Write(t.Context(), "run", rec("api", "orders_test.go", refund+"\n# setup\napi/seed#func:Users\n"), rec("e2e", "e2e/checkout.spec.ts", total+"\n"),
		rec("api", "empty_test.go", ""),
		gitstore.Update{Ref: id.EndRef("api"), Files: map[string][]byte{"end.json": []byte("{}")}}); err != nil {
		t.Fatal(err)
	}

	got, src, err := lists.For(t.Context(), s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	if _, made := got["e2e/e2e/checkout.spec.ts"]; !slices.Equal(got["api/orders_test.go"], ids(create, refund)) || made {
		t.Fatalf("lists %v", got)
	}

	if !slices.Equal(got["api/_setup"], ids("api/seed#func:Users")) {
		t.Fatalf("setup %v", got["api/_setup"])
	}

	if _, ok := got["api/empty_test.go"]; ok {
		t.Fatal("an empty recording is no data, never an empty list")
	}

	want := []lists.LiveRun{{Run: "pr", Change: "pr-7", Commit: c[1][:12], Slices: 2}}
	if src.Back != 1 || !slices.Equal(src.Live, want) {
		t.Fatalf("source %+v", src)
	}
}

func TestAttachMergesIntoTheTargetsLists(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{
		"api/orders_test.go":       ids(create, total),
		"e2e/e2e/checkout.spec.ts": ids(total),
	})

	pr := lists.Attach{
		Repo: repo, Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567", To: c[2][:10],
		Recordings: recording.Lists{"api/orders_test.go": ids(refund), "api/new_test.go": ids(create)},
		Shrinkable: map[string]bool{"api/orders_test.go": true},
	}

	got, err := pr.Do(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	if got.To != c[2] || got.Base.From != c[0] || got.Again || got.Slices != 2 || got.Grown != 1 {
		t.Fatalf("first attach: %+v", got)
	}

	want := recording.Lists{
		"api/orders_test.go":       ids(create, refund, total),
		"e2e/e2e/checkout.spec.ts": ids(total),
	}
	expect(t, s, repo, c[2], want)

	got, err = pr.Do(ctx, s)
	if err != nil || !got.Again {
		t.Fatalf("again: %+v %v", got, err)
	}

	expect(t, s, repo, c[2], want)

	main := lists.Attach{
		Repo: repo, Run: "nightly", Change: "main", Commit: c[2], To: c[2], Trusted: true,
		Recordings: recording.Lists{
			"api/orders_test.go": ids(refund),
			"api/new_test.go":    ids(total),
		},
		Shrinkable: map[string]bool{"api/orders_test.go": true, "e2e/e2e/checkout.spec.ts": true},
		Whole:      map[string]bool{"api/orders_test.go": true, "api/new_test.go": true, "e2e/e2e/checkout.spec.ts": true},
	}

	got, err = main.Do(ctx, s)
	if err != nil || got.Again || got.Base.Back != 0 || got.Grown != 2 {
		t.Fatalf("second run: %+v %v", got, err)
	}

	expect(t, s, repo, c[2], recording.Lists{
		"api/orders_test.go":       ids(refund),
		"api/new_test.go":          ids(total),
		"e2e/e2e/checkout.spec.ts": ids(total),
	})

	att, err := lists.Attachments(ctx, s, c[2])
	if err != nil {
		t.Fatal(err)
	}

	wantAtt := []lists.Attachment{
		{Run: "nightly", Change: "main", Commit: c[2]},
		{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"},
	}
	if !slices.Equal(att, wantAtt) {
		t.Fatalf("attachments %+v", att)
	}

	expect(t, s, repo, c[0], recording.Lists{"api/orders_test.go": ids(create, total), "e2e/e2e/checkout.spec.ts": ids(total)})
}

func TestAttachRefusesWhatItCantFollow(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)

	a := lists.Attach{Repo: repo, Run: "pr", Change: "pr-7", Commit: c[1], To: "feedfacefeedfacefeedfacefeedfacefeedface"}
	if _, err := a.Do(t.Context(), s); err == nil || !strings.Contains(err.Error(), "fetch it first") {
		t.Fatalf("unknown target: %v", err)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	git(t, filepath.Dir(shallow), "clone", "--quiet", "--depth", "1", "file://"+repo, shallow)

	a = lists.Attach{Repo: shallow, Run: "pr", Change: "pr-7", Commit: c[1], To: c[2]}
	if _, err := a.Do(t.Context(), s); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("shallow checkout: %v", err)
	}

	if _, err := (lists.Attach{Repo: repo, To: c[2]}).Do(t.Context(), s); err == nil {
		t.Fatal("no run named")
	}
}

func expect(t *testing.T, s *gitstore.Store, repo, commit string, want recording.Lists) {
	t.Helper()

	got, src, err := lists.Nearest(t.Context(), s, repo, commit)
	if err != nil {
		t.Fatal(err)
	}

	if src.Back != 0 || len(got) != len(want) {
		t.Fatalf("lists of %s from %+v: %v, want %v", commit[:12], src, got, want)
	}

	for k, ids := range want {
		if !slices.Equal(got[k], ids) {
			t.Fatalf("%s: %v, want %v", k, got[k], ids)
		}
	}
}

func TestASetupIsKeptApart(t *testing.T) {
	recs, _, err := lists.Recordings(map[string][]byte{
		"e2e/e2e/checkout.spec.ts.txt": []byte(create + "\n# setup\napi/seed#func:Users\n"),
		"e2e/e2e/orders.spec.ts.txt":   []byte(total + "\n"),
		"api/orders_test.go.txt":       []byte(refund + "\n# setup\napi/seed#func:Orders\n"),
		"api/empty_test.go.txt":        nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := recording.Lists{
		"e2e/e2e/checkout.spec.ts": ids(create),
		"e2e/e2e/orders.spec.ts":   ids(total),
		"e2e/_setup":               ids("api/seed#func:Users"),
		"api/orders_test.go":       ids(refund),
		"api/_setup":               ids("api/seed#func:Orders"),
	}

	if len(recs) != len(want) {
		t.Fatalf("recordings %v", recs)
	}

	for k, v := range want {
		if !slices.Equal(recs[k], v) {
			t.Fatalf("%s: %v, want %v", k, recs[k], v)
		}
	}

	repo, c := history(t, 2)
	s := remote(t)
	putLists(t, s, c[0], recording.Lists{"e2e/_setup": ids("api/seed#func:Old")})

	partial := lists.Attach{Repo: repo, Run: "nightly", Change: "main", Commit: c[1], To: c[1], Recordings: recs, Trusted: true,
		Shrinkable: map[string]bool{"e2e/e2e/checkout.spec.ts": true}, Whole: map[string]bool{"e2e/e2e/checkout.spec.ts": true}}
	if _, err := partial.Do(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	got, _, err := lists.Nearest(t.Context(), s, repo, c[1])
	if err != nil || !slices.Equal(got["e2e/_setup"], ids("api/seed#func:Old", "api/seed#func:Users")) {
		t.Fatalf("a failed slice keeps the setup growing: %v %v", got["e2e/_setup"], err)
	}

	whole := lists.Attach{Repo: repo, Run: "nightly", Change: "main", Commit: c[0], To: c[1], Recordings: recs, Trusted: true,
		Shrinkable: map[string]bool{"e2e/e2e/checkout.spec.ts": true, "e2e/e2e/orders.spec.ts": true},
		Whole:      map[string]bool{"e2e/e2e/checkout.spec.ts": true, "e2e/e2e/orders.spec.ts": true}}
	if _, err := whole.Do(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	if got, _, err = lists.Nearest(t.Context(), s, repo, c[1]); err != nil || !slices.Equal(got["e2e/_setup"], ids("api/seed#func:Users")) {
		t.Fatalf("every slice passed: %v %v", got["e2e/_setup"], err)
	}
}

func TestMain(m *testing.M) { storetest.Both(m) }

func TestAttachedRunsLiveInTheListsRef(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)
	ctx := t.Context()

	old := lists.Attachment{Run: "main", Change: "main", Commit: c[1]}
	note := []byte(`{"run": "main", "change": "main", "commit": "` + c[1] + `"}` + "\n")

	if _, err := s.Write(ctx, "before", gitstore.Update{Ref: lists.Ref(c[1]), Files: lists.Encode(recording.Lists{"api/orders_test.go": ids(create)})},
		gitstore.Update{Ref: lists.AttachedRef(c[1]), Files: map[string][]byte{old.File(): note}}); err != nil {
		t.Fatal(err)
	}

	if got, err := lists.Attachments(ctx, s, c[1]); err != nil || !slices.Equal(got, []lists.Attachment{old}) {
		t.Fatalf("before: %+v %v", got, err)
	}

	again := lists.Attach{Repo: repo, Run: "main", Change: "main", Commit: c[1], To: c[1]}
	if got, err := again.Do(ctx, s); err != nil || !got.Again {
		t.Fatalf("again: %+v %v", got, err)
	}

	pr := lists.Attach{Repo: repo, Run: "pr", Change: "pr-3", Commit: c[0], To: c[1], Recordings: recording.Lists{"api/orders_test.go": ids(total)}}
	if _, err := pr.Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	ref, ok, err := s.First(ctx, []string{lists.Ref(c[1])})
	if err != nil || !ok {
		t.Fatal(err)
	}

	files, err := s.Files(ctx, ref.Commit)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"_attached/" + old.File(), "_attached/pr/pr-3/" + c[0] + ".json"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the lists ref lacks %s: %v", want, slices.Sorted(maps.Keys(files)))
		}
	}

	got, err := lists.Attachments(ctx, s, c[1])
	if err != nil || len(got) != 2 {
		t.Errorf("after: %+v %v", got, err)
	}

	expect(t, s, repo, c[1], recording.Lists{"api/orders_test.go": ids(create, total)})

	landed, err := attached.Since(ctx, s, time.Time{})
	if err != nil || len(landed) != 1 || len(landed[0].Runs) != 2 || landed[0].Commit != c[1] {
		t.Errorf("since: %+v %v", landed, err)
	}
}

func TestALiveRunNeverMakesASlicesList(t *testing.T) {
	repo, c := history(t, 2)
	s := remote(t)

	putLists(t, s, c[0], recording.Lists{"api/orders_test.go": ids(create)})

	id := report.ID{Run: "pr", Change: "pr-8", Commit: c[1]}
	if _, err := s.Write(t.Context(), "run",
		gitstore.Update{Ref: id.StateRef("api", "orders_test.go"), Files: map[string][]byte{"state.json": []byte(`{"state": "passed"}`), "recording.txt": []byte(refund + "\n")}},
		gitstore.Update{Ref: id.StateRef("api", "new_test.go"), Files: map[string][]byte{"state.json": []byte(`{"state": "passed"}`), "recording.txt": []byte(total + "\n# setup\napi/seed#func:Users\n")}},
	); err != nil {
		t.Fatal(err)
	}

	got, _, err := lists.For(t.Context(), s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got["api/orders_test.go"], ids(create, refund)) {
		t.Errorf("a listed slice: %v", got["api/orders_test.go"])
	}

	if l, made := got["api/new_test.go"]; made {
		t.Errorf("a live run made a list for a slice without one: %v", l)
	}

	if !slices.Equal(got["api/_setup"], ids("api/seed#func:Users")) {
		t.Errorf("the suite's setup list: %v", got["api/_setup"])
	}
}

func TestAFailedSlicesRecordingNeverMakesAList(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	putLists(t, s, c[0], recording.Lists{"api/orders_test.go": ids(create)})

	first := lists.Attach{
		Repo: repo, Run: "nightly", Change: "main", Commit: c[1], To: c[1], Trusted: true,
		Recordings: recording.Lists{
			"api/orders_test.go": ids(refund), // failed, has a list: adds
			"api/panics_test.go": ids(total),  // failed part way, no list: none
		},
		CannotTell: lists.Marks{"api/panics_test.go": {{Plugin: "go-correlator", Reason: "no coverprofile"}}},
		Shrinkable: map[string]bool{},
		Whole:      map[string]bool{},
	}

	if _, err := first.Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	expect(t, s, repo, c[1], recording.Lists{"api/orders_test.go": ids(create, refund)})

	got, src, err := lists.For(ctx, s, repo, c[1])
	if err != nil {
		t.Fatal(err)
	}

	if _, listed := got["api/panics_test.go"]; listed || len(src.CannotTell["api/panics_test.go"]) == 0 {
		t.Fatalf("the failed slice: lists %v, marks %v", got, src.CannotTell)
	}

	pr := lists.Attach{
		Repo: repo, Run: "pr", Change: "pr-9", Commit: "0123456789abcdef0123456789abcdef01234567", To: c[2][:10],
		Recordings: recording.Lists{"api/new_test.go": ids(create)},
	}
	if _, err := pr.Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	expect(t, s, repo, c[2], recording.Lists{"api/orders_test.go": ids(create, refund)})

	second := lists.Attach{
		Repo: repo, Run: "nightly", Change: "main", Commit: c[2], To: c[2], Trusted: true,
		Recordings: recording.Lists{"api/panics_test.go": ids(total, create)},
		Whole:      map[string]bool{"api/panics_test.go": true},
	}

	if _, err := second.Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	expect(t, s, repo, c[2], recording.Lists{"api/orders_test.go": ids(create, refund), "api/panics_test.go": ids(create, total)})

	if _, src, err := lists.For(ctx, s, repo, c[2]); err != nil || len(src.CannotTell["api/panics_test.go"]) != 0 {
		t.Fatalf("the mark after it passed: %v %v", src.CannotTell, err)
	}
}

func TestALiveRunsMarkYieldsToANewerRecordingAndItsLineage(t *testing.T) {
	repo, c := history(t, 3)
	s := remote(t)
	ctx := t.Context()

	git(t, repo, "checkout", "--quiet", "-b", "side", c[0])
	git(t, repo, "commit", "--quiet", "--allow-empty", "-m", "side")
	side := git(t, repo, "rev-parse", "HEAD")

	putLists(t, s, c[2], recording.Lists{"api/orders_test.go": ids(create), "api/total_test.go": ids(total)})

	marked := recording.Recording{Observed: true, CannotTell: []recording.Mark{{Plugin: "react", Reason: "no recording"}}}.Format()

	write := func(commit, slice string, body []byte) {
		t.Helper()

		id := report.ID{Run: "nightly", Change: "main", Commit: commit}
		if _, err := s.Write(ctx, "run", gitstore.Update{Ref: id.StateRef("api", slice), Files: map[string][]byte{
			"state.json": []byte(`{"state": "failed"}`), "recording.txt": body,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	write(c[1], "orders_test.go", marked)
	write(c[1], "new_test.go", marked)
	write(side, "total_test.go", marked)
	write(side, "orders_test.go", []byte(refund+"\n"))

	got, src, err := lists.For(ctx, s, repo, c[2])
	if err != nil {
		t.Fatal(err)
	}

	if len(src.CannotTell) != 1 || len(src.CannotTell["api/new_test.go"]) == 0 || !slices.Equal(got["api/orders_test.go"], ids(create, refund)) {
		t.Fatalf("marks %v, lists %v", src.CannotTell, got)
	}
}

func TestARunOfEverythingOutlivesNoOlderRecording(t *testing.T) {
	repo, c := history(t, 4)
	s := remote(t)
	ctx := t.Context()

	git(t, repo, "checkout", "--quiet", "-b", "side", c[0])
	git(t, repo, "commit", "--quiet", "--allow-empty", "-m", "side")
	side := git(t, repo, "rev-parse", "HEAD")

	const (
		slice    = "api/orders_test.go"
		moved    = "api/orders#func:Moved"
		seedOld  = "api/seed#func:Old"
		seedNew  = "api/seed#func:New"
		sideUnit = "api/orders#func:Side"
	)

	putLists(t, s, c[0], recording.Lists{slice: ids(create, moved), "api/_setup": ids(seedOld)})

	live := func(run, commit, state string, rec recording.Recording) {
		t.Helper()

		id := report.ID{Run: run, Change: "main", Commit: commit}
		if _, err := s.Write(ctx, "run", gitstore.Update{Ref: id.StateRef("api", "orders_test.go"), Files: map[string][]byte{
			"state.json": []byte(`{"state": "` + state + `"}`), "recording.txt": rec.Format(),
		}}); err != nil {
			t.Fatal(err)
		}
	}

	live("main", c[0], "passed", recording.Recording{Observed: true, Own: []string{create, moved}, Setup: []string{seedOld}})
	live("main", c[1], "failed", recording.Recording{Observed: true, Own: []string{refund}, Setup: []string{seedOld}})

	before, _, err := lists.For(ctx, s, repo, c[2])
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(before[slice], ids(create, moved, refund)) {
		t.Fatalf("before the run of everything: %v", before[slice])
	}

	whole := recording.Recording{Observed: true, Own: []string{create}, Setup: []string{seedNew}}
	live("nightly", c[2], "passed", whole)

	recs, _, err := lists.Recordings(map[string][]byte{slice + ".txt": whole.Format()})
	if err != nil {
		t.Fatal(err)
	}

	all := map[string]bool{slice: true}
	if _, err := (lists.Attach{Repo: repo, Run: "nightly", Change: "main", Commit: c[2], To: c[2], Recordings: recs, Shrinkable: all, Whole: all, Trusted: true}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	live("main", c[2], "failed", recording.Recording{Observed: true, CannotTell: []recording.Mark{{Plugin: "react", Reason: "no recording"}}})

	live("main", c[3], "failed", recording.Recording{Observed: true, Own: []string{total}})
	live("pr", side, "passed", recording.Recording{Observed: true, Own: []string{sideUnit}})

	for _, at := range []string{c[2], c[3]} {
		got, src, err := lists.For(ctx, s, repo, at)
		if err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(got[slice], ids(create, sideUnit, total)) {
			t.Errorf("at %s the list is %v: the older runs' units are back, or a later one is missing", at[:7], got[slice])
		}

		if !slices.Equal(got["api/_setup"], ids(seedNew)) {
			t.Errorf("at %s the setup list is %v", at[:7], got["api/_setup"])
		}

		if len(src.CannotTell) != 0 {
			t.Errorf("at %s a run of the replaced tree still marks: %v", at[:7], src.CannotTell)
		}

		for _, r := range src.Live {
			if r.Commit == c[0][:12] || r.Commit == c[1][:12] {
				t.Errorf("at %s the run %s %s is named as adding, and adds nothing", at[:7], r.Run, r.Commit)
			}
		}
	}

	later, _, err := lists.Recordings(map[string][]byte{slice + ".txt": recording.Recording{Observed: true, Own: []string{total}}.Format()})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (lists.Attach{Repo: repo, Run: "main", Change: "main", Commit: c[3], To: c[3], Recordings: later, Whole: all, Trusted: true}).Do(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, src, err := lists.For(ctx, s, repo, c[3])
	if err != nil {
		t.Fatal(err)
	}

	if src.Back != 0 || !slices.Equal(got[slice], ids(create, sideUnit, total)) || !slices.Equal(got["api/_setup"], ids(seedNew)) {
		t.Fatalf("after a run that adds: %+v %v %v", src, got[slice], got["api/_setup"])
	}
}
