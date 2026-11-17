package report

import (
	"strings"
	"testing"
)

func TestALevelTheConfigSetShowsInThePlanAndTheAlarm(t *testing.T) {
	set := NarrowingSet{Name: "fields", OffAt: 4, Declared: 2, By: "plugin go", Note: "every field read through reflection is declared"}

	r := Run{Name: "pr", Commit: "0123456789ab", Narrowings: []NarrowingSet{set}, Suites: []Suite{{
		Name: "e2e", Slices: []Slice{{Name: "e2e/a.spec.ts", State: Passed}},
		Narrowed: []Narrowed{
			{Kind: "narrowed", Narrowing: "fields", OffAt: 4, Reached: 3, Dropped: []string{"e2e/b.spec.ts"}},
			{Kind: "narrowed", Narrowing: "methods", OffAt: 3, Reached: 3, Dropped: []string{"e2e/c.spec.ts"}},
		},
	}}}

	body := Comment(r, 7, List)
	if !strings.Contains(body, "- `fields` left out 1; they would run from narrowing level 4, off from 4 by the config "+
		"(narrowings.fields: every field read through reflection is declared); plugin go says 2.\n") ||
		!strings.Contains(body, "- `methods` left out 1; they would run from narrowing level 3.\n") {
		t.Errorf("the plan:\n%s", body)
	}

	nightly := Run{Name: "nightly", Commit: "abcdef012345", Suites: []Suite{{
		Name: "e2e", Slices: []Slice{{Name: "e2e/b.spec.ts", State: Failed}},
	}}, Net: &Net{Alarm: 0, Traces: []Trace{
		{Suite: "e2e", Slice: "e2e/b.spec.ts", Run: "pr", Commit: "0123456789ab", Kind: "narrowed", Narrowing: "fields", OffAt: 4, Set: &set},
	}}}

	body = Comment(nightly, 0, List)
	for _, want := range []string{
		"**1 failures trace to narrowed changes: raise narrowing to 4, or lower `narrowings.fields.off_at`, which the config set.**",
		"left it out: narrowed by `fields`, off from 4 by the config (narrowings.fields: every field read through reflection is declared); plugin go says 2\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
}
