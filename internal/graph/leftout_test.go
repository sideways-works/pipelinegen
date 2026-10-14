package graph

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func forwardLeftOut(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice, n string, selected map[Key]bool) map[Key]bool {
	t.Helper()

	changed := func(id string) bool {
		ub, inB := b.Units[id]
		uh, inH := h.Units[id]

		return inB != inH || ub.Digest != uh.Digest
	}

	out := map[Key]bool{}

	for _, tr := range []*deps.Tree{b, h} {
		got, err := (&plugins.Set{}).Bind(context.Background(), t.TempDir(), cloneTree(tr), ss)
		if err != nil {
			t.Fatal(err)
		}

		for _, d := range got {
			k := Key{Suite: d.Suite, Name: d.Name, Width: d.Width}
			if selected[k] {
				continue
			}

			for _, id := range d.Dropped[n] {
				if changed(id) {
					out[k] = true
				}
			}
		}
	}

	return out
}

func TestANarrowingLeavesOutWhatTheForwardClosureSaysItDropped(t *testing.T) {
	const n = "wide-test"

	left := 0

	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 13)) //nolint:gosec // a test's repeatable trees
		units := 5 + r.IntN(30)
		b, h := randomTrees(r, units, 1+r.IntN(4), r.IntN(2*units))

		var wide []plugin.Link

		for range r.IntN(units) {
			from, to := r.IntN(units), r.IntN(units)
			if from != to {
				wide = append(wide, plugin.Link{From: fmt.Sprintf("u:%03d", from), To: fmt.Sprintf("u:%03d", to), Narrowing: n})
			}
		}

		b.RuledOut = map[string][]plugin.Link{n: wide}
		h.RuledOut = map[string][]plugin.Link{n: wide}

		ss := randomSlices(r, units, 1+r.IntN(6))

		selected := forwardSelected(t, b, h, ss)
		want := forwardLeftOut(t, b, h, ss, n, selected)

		got, err := Select(context.Background(), b, h, ss, []string{Executed, Dependents}, Options{})
		if err != nil {
			t.Fatal(err)
		}

		for _, k := range allKeys(ss) {
			paths, out := got.LeftOut[n][k]

			// the forward records list units only, so they miss the group a
			// unit that can't be told widens to
			if out && !want[k] && throughCannotTell(paths) {
				continue
			}

			if out != want[k] {
				t.Fatalf("seed %d: %v left out = %v, forward closure %v\nbase %v\nhead %v\nwide %v\nslices %+v",
					seed, k, out, want[k], b.Links, h.Links, wide, ss)
			}

			if want[k] {
				left++
			}
		}
	}

	if left == 0 {
		t.Fatal("no narrowing left anything out: the test proves nothing")
	}
}

func throughCannotTell(paths []Path) bool {
	for _, p := range paths {
		for _, st := range p.Steps {
			if st.Kind == KindCannotTell {
				return true
			}
		}
	}

	return false
}

func TestAFileBoundByItsUnitsLeavesOutTheSlicesThatDidntTouchTheChangedUnit(t *testing.T) {
	units := func(d string) *deps.Tree {
		tr := tree(map[string]string{"api:createNote": "1", "api:deleteNote": d, "go:h#func:Create": "1"},
			plugin.Link{From: "go:h#func:Create", To: "api:createNote", Kind: plugin.LinkUses})
		tr.ByUnits = map[string]string{"api/openapi.yaml": "document-units"}
		tr.FileUnits = map[string][]string{"api/openapi.yaml": {"api:createNote", "api:deleteNote"}}

		return tr
	}

	ss := []plugins.BindSlice{{
		Suite: "s", Name: "create_test.go", HasList: true, List: []string{"go:h#func:Create"},
		Data: []string{"//api:openapi.yaml"},
	}}

	got, err := Select(context.Background(), units("1"), units("2"), ss, []string{Executed}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Selected) != 0 {
		t.Fatalf("selected %v, want nothing: the slice touched createNote only", got.Selected)
	}

	if _, ok := got.LeftOut["document-units"][Key{Suite: "s", Name: "create_test.go", Width: Executed}]; !ok {
		t.Fatalf("left out %v, want create_test.go under document-units", got.LeftOut)
	}
}
