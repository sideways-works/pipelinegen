package report

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// buildWriter writes a pull request's builds into a store, each at a time of its own.
type buildWriter struct {
	t     *testing.T
	s     *gitstore.Store
	clock time.Time
}

func newBuildWriter(t *testing.T) *buildWriter {
	t.Helper()

	w := &buildWriter{t: t, s: testStore(t, bareRemote(t)), clock: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	w.s.Clock = func() time.Time { return w.clock }

	return w
}

func (w *buildWriter) write(ups ...gitstore.Update) {
	w.t.Helper()

	w.clock = w.clock.Add(time.Minute)

	if _, err := w.s.Write(w.t.Context(), "test", ups...); err != nil {
		w.t.Fatal(err)
	}
}

func prID(change, commit string) ID { return ID{Run: "pr", Change: change, Commit: commit} }

func (w *buildWriter) plan(change, commit, plan string) {
	w.t.Helper()
	w.write(gitstore.Update{Ref: prID(change, commit).PlanRef(), Files: map[string][]byte{planFileName: []byte(plan)}})
}

func (w *buildWriter) state(change, commit, slice string, st State, tests *int) {
	w.t.Helper()

	data, err := json.Marshal(StateFile{Label: "//e2e:" + slice, State: st, Tests: tests})
	if err != nil {
		w.t.Fatal(err)
	}

	w.write(gitstore.Update{Ref: prID(change, commit).StateRef("e2e", slice), Files: map[string][]byte{stateFile: data}})
}

func carriedPlan(head, carriedFrom string, dropped ...string) string {
	narrowed := ""

	if carriedFrom != "" {
		names, err := json.Marshal(dropped)
		if err != nil {
			panic(err)
		}

		narrowed = `, "narrowed": [{"kind": "narrowed", "narrowing": "passed-since", "dropped": ` + string(names) + `, "carried_from": "` + carriedFrom + `"}]`
	}

	return `{"base": "` + strings.Repeat("b", 40) + `", "head": "` + head + `", "merged": "MERGED", "suites": [{"suite": "e2e"` + narrowed + `}]}`
}

func passNames(b Build) []string { return slices.Sorted(maps.Keys(b.Passes)) }

func TestTheLastBuildIsTheNewestWithResults(t *testing.T) {
	w := newBuildWriter(t)
	c1, c2, c3, c4 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40), strings.Repeat("4", 40)
	zero := 0

	if _, ok, err := LastBuild(t.Context(), w.s, "pr", "pr-7", c1); ok || err != nil {
		t.Fatalf("no build yet: %v %v", ok, err)
	}

	w.plan("pr-7", c1, strings.Replace(carriedPlan(strings.Repeat("a", 40), ""), "MERGED", c1, 1))
	w.state("pr-7", c1, "e2e.a", Passed, nil)
	w.state("pr-7", c1, "e2e.b", Passed, nil)

	// the next build: b failed, c passed with no tests, d never ran, e is still running, a was carried
	w.plan("pr-7", c2, strings.Replace(carriedPlan(strings.Repeat("c", 40), c1, "e2e.a"), "MERGED", c2, 1))
	w.state("pr-7", c2, "e2e.b", Failed, nil)
	w.state("pr-7", c2, "e2e.c", Passed, &zero)
	w.state("pr-7", c2, "e2e.d", NotRun, nil)
	w.state("pr-7", c2, "e2e.e", Running, nil)
	w.state("pr-7", c2, "e2e.f", Passed, nil)

	// a later build that has no result yet is passed over
	w.plan("pr-7", c3, strings.Replace(carriedPlan(strings.Repeat("d", 40), ""), "MERGED", c3, 1))

	// another pull request's newer build counts for nothing here
	w.plan("pr-8", c4, strings.Replace(carriedPlan(strings.Repeat("e", 40), ""), "MERGED", c4, 1))
	w.state("pr-8", c4, "e2e.b", Passed, nil)

	b, ok, err := LastBuild(t.Context(), w.s, "pr", "pr-7", c4)
	if err != nil || !ok {
		t.Fatalf("the last build: %v %v", ok, err)
	}

	if b.Commit != c2[:12] || b.Merged != c2 || b.Head != strings.Repeat("c", 40) || b.Base != strings.Repeat("b", 40) {
		t.Fatalf("the last build %+v, want %s's", b, c2[:12])
	}

	if got := passNames(b); !slices.Equal(got, []string{"e2e/e2e.a", "e2e/e2e.f"}) {
		t.Fatalf("passes %v: only f passed with tests run, and a was carried over", got)
	}

	if !b.Passes["e2e/e2e.a"].Carried || b.Passes["e2e/e2e.f"].Carried || b.Passes["e2e/e2e.f"].Label != "//e2e:e2e.f" {
		t.Fatalf("passes %+v", b.Passes)
	}

	// the build of the commit being planned is never its own last build
	if b, ok, err := LastBuild(t.Context(), w.s, "pr", "pr-7", c2); err != nil || !ok || b.Commit != c1[:12] {
		t.Fatalf("planning %s again: %+v %v %v", c2[:12], b, ok, err)
	}
}

func TestACarriedSliceThatRanStandsByItsState(t *testing.T) {
	w := newBuildWriter(t)
	c1 := strings.Repeat("1", 40)

	w.plan("pr-7", c1, strings.Replace(carriedPlan(strings.Repeat("a", 40), strings.Repeat("0", 40), "e2e.a", "e2e.b"), "MERGED", c1, 1))
	w.state("pr-7", c1, "e2e.a", Failed, nil)

	b, ok, err := LastBuild(t.Context(), w.s, "pr", "pr-7", strings.Repeat("9", 40))
	if err != nil || !ok || !slices.Equal(passNames(b), []string{"e2e/e2e.b"}) {
		t.Fatalf("%+v %v %v", b, ok, err)
	}
}

func TestNothingIsCarriedFromABuildWithUnreadableRecords(t *testing.T) {
	c1, c2, head := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("9", 40)

	for name, broken := range map[string]func(w *buildWriter){
		"its plan is gone": func(w *buildWriter) { w.state("pr-7", c2, "e2e.a", Passed, nil) },
		"its plan can't be parsed": func(w *buildWriter) {
			w.plan("pr-7", c2, `{"head": `)
			w.state("pr-7", c2, "e2e.a", Passed, nil)
		},
		"a state can't be parsed": func(w *buildWriter) {
			w.plan("pr-7", c2, strings.Replace(carriedPlan(strings.Repeat("c", 40), ""), "MERGED", c2, 1))
			w.state("pr-7", c2, "e2e.a", Passed, nil)
			w.write(gitstore.Update{Ref: prID("pr-7", c2).StateRef("e2e", "e2e.b"), Files: map[string][]byte{stateFile: []byte("{")}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBuildWriter(t)

			// an older build with a pass: never what an unreadable newer one falls back to
			w.plan("pr-7", c1, strings.Replace(carriedPlan(strings.Repeat("a", 40), ""), "MERGED", c1, 1))
			w.state("pr-7", c1, "e2e.a", Passed, nil)

			broken(w)

			if b, ok, err := LastBuild(t.Context(), w.s, "pr", "pr-7", head); !errors.Is(err, ErrUnreadable) || ok {
				t.Fatalf("%+v %v %v", b, ok, err)
			}
		})
	}
}

func TestCarriedSlicesAreCountedWithTheirBuild(t *testing.T) {
	from := strings.Repeat("8", 40)
	r := Run{Name: "pr", Commit: "c", Suites: []Suite{{
		Name: "e2e", Untouched: make([]string, 3),
		Slices: []Slice{{Name: "e2e/a.spec.ts", State: Passed}, {Name: "e2e/b.spec.ts", State: Failed}},
		Narrowed: []Narrowed{{
			Kind: "narrowed", Narrowing: "passed-since", OffAt: 4, Reached: 257, CarriedFrom: from,
			Reason: "carried over from " + from[:12], Dropped: make([]string, 255),
		}},
	}, {
		Name: "backend", Narrowed: []Narrowed{{Kind: "narrowed", Narrowing: "passed-since", OffAt: 4, CarriedFrom: from, Dropped: make([]string, 729)}},
	}}}

	if _, desc := r.Suites[0].Status(); !strings.HasSuffix(desc, ", 255 carried over from "+from[:12]) || strings.Contains(desc, "left out") {
		t.Fatalf("the suite's status %q", desc)
	}

	if n, c := r.Carried(); n != 984 || c != from {
		t.Fatalf("carried %d from %s", n, c)
	}

	if _, desc := r.RunStatus(); !strings.Contains(desc, "984 carried over from "+from[:12]) || len(desc) > maxDescription {
		t.Fatalf("the run's status %q", desc)
	}

	for _, format := range []Format{List, Mermaid} {
		body := Comment(r, 1, format)
		if !strings.Contains(body, "`passed-since` carried over 255 as passed from the last build, `"+from[:12]+"`; they would run from narrowing level 4.") &&
			!strings.Contains(body, "255 carried over from "+from[:12]+", run from level 4") {
			t.Errorf("%s:\n%s", format, body)
		}
	}
}
