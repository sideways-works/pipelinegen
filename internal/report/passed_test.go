package report

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

func TestPassesAreTheNewestWithTestsRun(t *testing.T) {
	ctx := t.Context()
	s := testStore(t, bareRemote(t))

	c1, c2, c3, c4, c5 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40), strings.Repeat("4", 40), strings.Repeat("5", 40)
	zero, five := 0, 5
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	var ups []gitstore.Update

	plan := func(c, head string) {
		ups = append(ups, gitstore.Update{Ref: ID{Run: "main", Change: "main", Commit: c}.PlanRef(), Files: map[string][]byte{
			planFileName: []byte(`{"head": "` + head + `", "suites": []}`),
		}})
	}

	state := func(c, slice string, st State, tests *int) {
		data, err := json.Marshal(StateFile{Label: "//e2e:" + slice, State: st, Tests: tests, Updated: at})
		if err != nil {
			t.Fatal(err)
		}

		ups = append(ups, gitstore.Update{Ref: ID{Run: "main", Change: "main", Commit: c}.StateRef("e2e", "e2e."+slice), Files: map[string][]byte{stateFile: data}})
	}

	plan(c1, c1)
	state(c1, "a", Passed, nil)
	state(c1, "b", Failed, nil)
	state(c1, "c", Passed, &zero)
	state(c1, "d", NotRun, nil)

	plan(c2, c2)
	state(c2, "a", Passed, nil)
	state(c2, "b", Passed, nil)
	state(c2, "c", Passed, &five)
	state(c2, "d", Passed, nil)

	// swept: the state outlived its plan
	state(c3, "e", Passed, nil)

	// another commit's run, beyond the commits asked about
	plan(c4, c4)
	state(c4, "f", Passed, nil)

	// a plan that tested another tree than the commit's
	plan(c5, c4)
	state(c5, "g", Passed, nil)

	// another run of the same commit
	ups = append(ups, gitstore.Update{Ref: ID{Run: "nightly", Change: "main", Commit: c1}.StateRef("e2e", "e2e.h"), Files: map[string][]byte{stateFile: []byte(`{"state": "passed"}`)}})

	if _, err := s.Write(ctx, "runs", ups...); err != nil {
		t.Fatal(err)
	}

	got, err := Passes(ctx, s, "main", "main", []string{c1, c2, c3, c5})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"e2e/e2e.a": c1, "e2e/e2e.b": c2, "e2e/e2e.c": c2, "e2e/e2e.d": c2}

	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("passes of %v, want %v", keys, slices.Sorted(maps.Keys(want)))
	}

	for k, c := range want {
		if got[k].Commit != c || got[k].Label != "//e2e:"+strings.TrimPrefix(k, "e2e/e2e.") || !got[k].At.Equal(at) {
			t.Errorf("%s: %+v, want a pass at %s", k, got[k], c[:12])
		}
	}
}
