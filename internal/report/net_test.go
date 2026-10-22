package report

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

func TestSampledRunsSayItAndNeverLookFull(t *testing.T) {
	r := Run{Name: "pr", Commit: "c", Suites: []Suite{{
		Name: "e2e", Untouched: make([]string, 235),
		Slices: []Slice{{Name: "e2e/a.spec.ts", State: Passed}},
		Narrowed: []Narrowed{{
			Kind: "sampled", Narrowing: "same-handler", OffAt: 2, Reached: 236,
			Reason: "the change is exercised the same by every test", Dropped: make([]string, 235),
		}},
	}}}

	state, desc := r.Suites[0].Status()
	if state != "success" || desc != "1 of 236 slices ran, all passed, same-handler left out 235" {
		t.Fatalf("%s %q", state, desc)
	}

	for _, format := range []Format{List, Mermaid} {
		body := Comment(r, 1, format)
		if !strings.Contains(body, "change reaches 236 slices; 1 run.") ||
			!strings.Contains(body, "The sample (`same-handler`) left 235 for the nightly; they would run from narrowing level 2. Why: the change is exercised the same by every test.") {
			t.Errorf("%s:\n%s", format, body)
		}
	}
}

func TestANightlyTracesFailuresToNarrowedChanges(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	gh.subjects["m1head"] = "Merge pull request #123 from x/search"
	gh.subjects["m2head"] = "Merge pull request #124 from x/orders"

	clock := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	at := func(r *Reporter) *Reporter {
		r.Store.Clock = func() time.Time { return clock }
		r.Now = r.Store.Clock
		r.Observer = fakeObserver{rec: []byte("x\n")}

		return r
	}

	nightly := func(commit string) *Reporter {
		r := at(newReporter(t, remote, gh, "list"))
		r.ID = ID{Run: "nightly", Change: "main", Commit: commit}

		return r
	}

	branch := func(commit, head string, dropped ...string) {
		r := at(newReporter(t, remote, gh, ""))
		r.ID = ID{Run: "main", Change: "main", Commit: commit}

		plan := `{"head": "` + head + `", "narrowing": 1, "suites": [{"suite": "e2e", "selected": [{"name": "e2e/a.spec.ts"}], "untouched": [],
			"narrowed": [{"kind": "sampled", "narrowing": "same-handler", "off_at": 2, "reached": 4, "dropped": ["` + strings.Join(dropped, `", "`) + `"]}]}]}`
		if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
			t.Fatal(err)
		}
	}

	all := `{"head": "h", "all": true, "suites": [{"suite": "e2e", "selected": [{"name": "e2e/a.spec.ts"}, {"name": "e2e/b.spec.ts"}, {"name": "e2e/c.spec.ts"}, {"name": "e2e/d.spec.ts"}], "untouched": []}]}`

	end := func(r *Reporter, failed ...string) {
		for _, name := range []string{"e2e/a.spec.ts", "e2e/b.spec.ts", "e2e/c.spec.ts", "e2e/d.spec.ts"} {
			result := `{"passed": true}`

			for _, f := range failed {
				if f == name {
					result = `{"passed": false, "failures": [{"test": "t"}]}`
				}
			}

			if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: name, Label: name, Result: writeResult(t, result), Coverage: []string{"c"}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	branch("m0000000000000", "m0head", "e2e/b.spec.ts")

	clock = clock.Add(time.Hour)
	n1 := nightly("n1000000000000")

	if err := WritePlan(ctx, n1.Store, n1.ID, []byte(all)); err != nil {
		t.Fatal(err)
	}

	end(n1)

	if ok, err := n1.Finish(ctx); err != nil || !ok {
		t.Fatalf("first nightly: %v %v", ok, err)
	}

	clock = clock.Add(time.Hour)

	branch("m1000000000000", "m1head", "e2e/b.spec.ts", "e2e/c.spec.ts")

	clock = clock.Add(time.Hour)

	branch("m2000000000000", "m2head", "e2e/d.spec.ts")

	clock = clock.Add(time.Hour)
	n2 := nightly("n2000000000000")

	if err := WritePlan(ctx, n2.Store, n2.ID, []byte(all)); err != nil {
		t.Fatal(err)
	}

	end(n2, "e2e/b.spec.ts", "e2e/c.spec.ts", "e2e/d.spec.ts")

	if ok, err := n2.Finish(ctx); err != nil || ok {
		t.Fatalf("second nightly: %v %v", ok, err)
	}

	st, ok := gh.status("n2000000000000", "pipelinegen/nightly")
	if !ok || st.State != "failure" || st.Description != "3 failures trace to narrowed changes: raise narrowing to 2" {
		t.Fatalf("run status: %+v", st)
	}

	snap, err := readSnapshot(ctx, n2.Store, n2.Config, n2.ID)
	if err != nil {
		t.Fatal(err)
	}

	run := buildRun(n2.ID, snap, nil)

	if run.Net, err = n2.net(ctx, run, snap); err != nil {
		t.Fatal(err)
	}

	body := Comment(run, 1, List)
	for _, want := range []string{
		"**3 failures trace to narrowed changes: raise narrowing to 2.**",
		"- `e2e e2e/b.spec.ts` first fails since `m10000000000` (Merge pull request #123 from x/search), whose main run left it out: sampled by `same-handler`, off from level 2",
		"- `e2e e2e/d.spec.ts` first fails since `m20000000000` (Merge pull request #124 from x/orders)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q:\n%s", want, body)
		}
	}

	if strings.Contains(body, "m00000000000") {
		t.Error("a change before the last passing nightly was named")
	}

	n2.Config.NarrowingAlarm = 5
	if run.Net, err = n2.net(ctx, run, snap); err != nil || run.Net.Alarm != 5 {
		t.Fatalf("narrowing_alarm 5: %v %v", run.Net, err)
	}

	if body := Comment(run, 1, List); strings.Contains(body, "raise narrowing") || !strings.Contains(body, "Failures that trace to narrowed changes:") {
		t.Fatalf("below the alarm:\n%s", body)
	}
}

func TestANightlyTracesFailuresToPullRequestsThatLanded(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	gh.subjects["landed1"] = "Merge pull request #9 from x/cart"

	clock := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	at := func(r *Reporter) *Reporter {
		r.Store.Clock = func() time.Time { return clock }
		r.Now = r.Store.Clock
		r.Observer = fakeObserver{rec: []byte("x\n")}

		return r
	}

	names := []string{"e2e/a.spec.ts", "e2e/b.spec.ts", "e2e/c.spec.ts"}
	all := `{"head": "h", "all": true, "suites": [{"suite": "e2e", "selected": [{"name": "e2e/a.spec.ts"}, {"name": "e2e/b.spec.ts"}, {"name": "e2e/c.spec.ts"}], "untouched": []}]}`

	nightly := func(commit string, failed ...string) bool {
		r := at(newReporter(t, remote, gh, "list"))
		r.ID = ID{Run: "nightly", Change: "main", Commit: commit}

		if err := WritePlan(ctx, r.Store, r.ID, []byte(all)); err != nil {
			t.Fatal(err)
		}

		for _, name := range names {
			result := `{"passed": true}`
			if slices.Contains(failed, name) {
				result = `{"passed": false, "failures": [{"test": "t"}]}`
			}

			if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: name, Label: name, Result: writeResult(t, result), Coverage: []string{"c"}}); err != nil {
				t.Fatal(err)
			}
		}

		ok, err := r.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}

		return ok
	}

	pull := func(change, commit string, dropped string, landedAs string) {
		r := at(newReporter(t, remote, gh, ""))
		r.ID = ID{Run: "pr", Change: change, Commit: commit}

		plan := `{"head": "merged-` + commit + `", "narrowing": 1, "suites": [{"suite": "e2e", "selected": [{"name": "e2e/a.spec.ts"}], "untouched": [],
			"narrowed": [{"kind": "narrowed", "narrowing": "fields", "off_at": 4, "reached": 3, "dropped": ["` + dropped + `"]}]}]}`
		if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
			t.Fatal(err)
		}

		if landedAs == "" {
			return
		}

		clock = clock.Add(time.Minute)

		a := attached.Attachment{Run: "pr", Change: change, Commit: commit}
		if _, err := r.Store.Write(ctx, "attached", gitstore.Update{Ref: attached.Ref(landedAs), Files: map[string][]byte{
			a.File(): []byte(`{"run": "pr", "change": "` + change + `", "commit": "` + commit + `"}`),
		}}); err != nil {
			t.Fatal(err)
		}
	}

	if !nightly("n1000000000000") {
		t.Fatal("first nightly failed")
	}

	clock = clock.Add(time.Hour)

	pull("pr-9", "p9c0000000000000", "e2e/b.spec.ts", "landed1")
	pull("pr-10", "p10c000000000000", "e2e/c.spec.ts", "")

	clock = clock.Add(time.Hour)

	if nightly("n2000000000000", "e2e/b.spec.ts", "e2e/c.spec.ts") {
		t.Fatal("second nightly passed")
	}

	r := at(newReporter(t, remote, gh, "list"))
	r.ID = ID{Run: "nightly", Change: "main", Commit: "n2000000000000"}

	snap, err := readSnapshot(ctx, r.Store, r.Config, r.ID)
	if err != nil {
		t.Fatal(err)
	}

	run := buildRun(r.ID, snap, nil)

	n, err := r.net(ctx, run, snap)
	if err != nil {
		t.Fatal(err)
	}

	if len(n.Traces) != 1 {
		t.Fatalf("traces %+v", n.Traces)
	}

	tr := n.Traces[0]
	if tr.Slice != "e2e/b.spec.ts" || tr.Run != "pr" || tr.Change != "pr-9" || tr.Subject != "Merge pull request #9 from x/cart" || tr.Narrowing != "fields" {
		t.Fatalf("trace %+v", tr)
	}
}

func TestASliceSeveralNarrowingsLeftOutCountsOnce(t *testing.T) {
	r := Run{Name: "pr", Commit: "c", Suites: []Suite{{
		Name: "go", Slices: []Slice{{Name: "a_test.go", State: Passed}},
		Narrowed: []Narrowed{
			{Kind: "narrowed", Narrowing: "method-sets", OffAt: 5, Reached: 3, Dropped: []string{"b_test.go", "c_test.go"}},
			{Kind: "narrowed", Narrowing: "unexported-methods", OffAt: 3, Reached: 2, Dropped: []string{"b_test.go"}},
		},
	}}}

	for _, format := range []Format{List, Mermaid} {
		body := Comment(r, 1, format)
		if !strings.Contains(body, "change reaches 3 slices; 1 run.") ||
			!strings.Contains(body, "- `method-sets` left out 2; they would run from narrowing level 5.") ||
			!strings.Contains(body, "- `unexported-methods` left out 1; they would run from narrowing level 3.") {
			t.Errorf("%s:\n%s", format, body)
		}
	}

	trace := func(commit, slice, narrowing string, offAt int) Trace {
		return Trace{Suite: "go", Slice: slice, Run: "main", Commit: commit, Kind: "narrowed", Narrowing: narrowing, OffAt: offAt}
	}

	net := &Net{Alarm: 1, Traces: []Trace{
		trace("c1", "b_test.go", "method-sets", 5), trace("c1", "b_test.go", "unexported-methods", 3),
		trace("c1", "d_test.go", "unexported-methods", 3),
	}}

	if level, ok := net.Raise(); !ok || level != 3 || net.Failing() != 2 {
		t.Errorf("raise %d %v, failing %d: want 3, two failures", level, ok, net.Failing())
	}

	net.Traces = append(net.Traces, trace("c2", "b_test.go", "method-sets", 5))
	if level, ok := net.Raise(); !ok || level != 5 || net.Failing() != 2 {
		t.Errorf("raise %d %v, failing %d: want 5, two failures", level, ok, net.Failing())
	}
}
