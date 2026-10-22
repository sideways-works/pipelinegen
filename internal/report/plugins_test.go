package report

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

func TestThePlansPluginsShowInTheStatusAndTheComment(t *testing.T) {
	const token = "warning-leaked-0123456789abcdef"

	secret.Add(token)

	r := Run{Name: "pr", Commit: "0123456789ab", Suites: []Suite{{
		Name: "e2e", Slices: []Slice{{Name: "e2e/a.spec.ts", State: Passed}},
		Narrowed: []Narrowed{{Kind: "narrowed", Narrowing: "fields", OffAt: 2, Reached: 3, Dropped: []string{"e2e/b.spec.ts", "e2e/c.spec.ts"}}},
	}}, Plugins: &Plugins{
		Calls: []PluginCall{
			{Plugin: "go", Call: "units", Asked: 3, Cached: 120},
			{Plugin: "go", Call: "bind", Asked: 1, Cached: 0},
		},
		Warnings: []string{"plugin ts could not tell: toolchain", "plugin x said " + token},
	}, PlanWarnings: []string{"the snapshot of c1 was hashed again"}}

	state, desc := r.RunStatus()
	if state != statusSuccess || !strings.Contains(desc, "3 warnings") || !strings.Contains(desc, "plugins: 120 of 124 files from the cache") {
		t.Fatalf("run status: %s %q", state, desc)
	}

	for _, format := range []Format{List, Mermaid} {
		body := Comment(r, 7, format)

		warn := strings.Index(body, "- plugin ts could not tell: toolchain")
		if warn < 0 || warn > strings.Index(body, "- **e2e**") || strings.Contains(body, token) ||
			!strings.Contains(body, "- the snapshot of c1 was hashed again\n") {
			t.Errorf("%s warnings:\n%s", format, body)
		}

		for _, want := range []string{
			"<summary>Plugins: 120 of 124 files answered from the cache</summary>",
			"| go | units | 3 | 120 |",
			"change reaches 3 slices; 1 run.",
			"- `fields` left out 2; they would run from narrowing level 2.\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: no %q in\n%s", format, want, body)
			}
		}
	}

	r.Suites[0].Narrowed = []Narrowed{{Kind: "sampled", Narrowing: "sample_on", OffAt: 4, Reached: 3, Dropped: []string{"e2e/b.spec.ts"},
		Reason: "web/app.css changed (sample_on web/**)"}}
	if body := Comment(r, 7, List); !strings.Contains(body, "- The sample (`sample_on`) left 1 for the nightly; they would run from narrowing level 4. Why: web/app.css changed (sample_on web/**).\n") {
		t.Errorf("sample_on:\n%s", body)
	}

	r.Suites[0].Narrowed = []Narrowed{{Kind: "narrowed", Narrowing: "fields", OffAt: 2, Reached: 3, Dropped: []string{"e2e/b.spec.ts", "e2e/c.spec.ts"}}}

	if body := Comment(r, 7, Mermaid); !strings.Contains(body, `s0 -.-> s0_n0["2 left out by fields, run from level 2"]:::untouched`) {
		t.Errorf("chart:\n%s", body)
	}
}

func TestThePlansWarningsReachTheJobsOutput(t *testing.T) {
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")

	var out strings.Builder

	r.Warn = &out

	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}]}],
	  "warnings": ["the snapshot of b was hashed again"], "plugins": {"warnings": ["plugin ts could not tell: toolchain"]}}`
	if err := WritePlan(t.Context(), r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !strings.Contains(got, "warning: the snapshot of b was hashed again\n") || !strings.Contains(got, "warning: plugin ts could not tell: toolchain\n") {
		t.Fatalf("job output:\n%s", got)
	}
}
