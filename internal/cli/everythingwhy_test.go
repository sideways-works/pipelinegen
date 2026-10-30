package cli

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/deps"
)

func TestASliceBoundToEverythingSaysSo(t *testing.T) {
	book := newPlanBook(nil)
	tree := &deps.Tree{Units: map[string]deps.Unit{"t:a": {ID: "t:a", Digest: "1"}}}
	marked := deps.Slice{Suite: "e2e", Name: "e2e/a.spec.ts", Width: "executed", Everything: true}

	book.keep("b", tree, []deps.Slice{marked})

	changed := &deps.Tree{Units: map[string]deps.Unit{"t:a": {ID: "t:a", Digest: "2"}}}
	book.keep("h", changed, []deps.Slice{marked})

	got, ok := book.why("b", "h", depsLabelPrefix+"slices:e2e/a.spec.ts", affected.RootCause{Label: depsLabelPrefix + "g/t:a"})
	if !ok || len(got) != 1 || got[0].Kind != affected.KindEverything || got[0].What != boundToEverything {
		t.Fatalf("reasons %+v (%v)", got, ok)
	}
}
