package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
)

func TestASliceWithoutAListSaysSo(t *testing.T) {
	book := newPlanBook(nil)
	book.noList("h", "e2e", "e2e/new.spec.ts")
	book.noList("h", "e2e", "e2e/new.spec.ts")

	plan := &affected.Plan{Head: "h", Suites: []affected.SuitePlan{{Suite: "e2e"}, {Suite: "go"}}}
	book.markUnlisted(plan)

	if !slices.Equal(plan.Suites[0].Unlisted, []string{"e2e/new.spec.ts"}) || plan.Suites[1].Unlisted != nil {
		t.Fatalf("unlisted %v %v", plan.Suites[0].Unlisted, plan.Suites[1].Unlisted)
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	var p whyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}

	p.Suites[0].Untouched = []string{depsLabelPrefix + "slices:e2e/new.spec.ts", depsLabelPrefix + "slices:e2e/old.spec.ts"}

	for slice, want := range map[string]string{
		"e2e/new.spec.ts": "it has no list yet, so it is bound to every unit the plugins list",
		"e2e/old.spec.ts": "nothing it is bound to changed",
	} {
		got := explain(&p, "e2e", slice)
		if len(got) != 1 || len(got[0].Reasons) == 0 || !strings.HasPrefix(got[0].Reasons[0].Text, want) {
			t.Errorf("%s: %+v", slice, got)
		}
	}
}
