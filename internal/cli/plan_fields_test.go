package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestTheReportReadsEveryFieldPlanningWrites(t *testing.T) {
	root, api, store := runEnv(t)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	plan := affected.Plan{
		Base: "b", Head: id.Commit, Narrowing: 3,
		Suites: []affected.SuitePlan{{
			Suite:     "e2e",
			Selected:  []bazel.Slice{{Name: "e2e.a", Label: "//e2e:a"}},
			Untouched: []string{"//e2e:c"},
			Narrowed: []affected.Narrowed{
				{Kind: affected.NarrowedSampled, Narrowing: "sample", OffAt: 4, Reason: "setup-only code changed", Reached: 2, Dropped: []string{"e2e.b"}},
				{Kind: affected.NarrowedBy, Narrowing: "fields", OffAt: 2, Reason: "a field it never reads", Reached: 2, Dropped: []string{"e2e.d"}},
			},
		}},
		Plugins: &plugins.Report{
			Calls:    []plugins.CallCount{{Plugin: "go", Call: "units", Asked: 2, Cached: 40}},
			Warnings: []string{"e2e/e2e.a: sql couldn't tell: no statement log; bound to everything"},
		},
		Lists: &lists.Source{Commit: "b", CannotTell: lists.Marks{
			"e2e/e2e.a": {{Plugin: "sql", Reason: "no statement log", Since: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}},
		}},
		Warnings: []string{"the snapshot of b was hashed again"},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	if err := report.WritePlan(t.Context(), store, id, data); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr"); !strings.Contains(got, "2 warnings") || !strings.Contains(got, "plugins: 40 of 42 files from the cache") {
		t.Fatalf("run status: %q", got)
	}

	if got := api.status("pipelinegen/pr/e2e"); !strings.Contains(got, "sample left out 1") || !strings.Contains(got, "fields left out 1") {
		t.Fatalf("suite status: %q", got)
	}

	body := api.comment(7)
	for _, want := range []string{
		"- the snapshot of b was hashed again",
		"- e2e/e2e.a: sql couldn't tell: no statement log; bound to everything",
		"The sample (`sample`) left 1 for the nightly; they would run from narrowing level 4",
		"- `fields` left out 1; they would run from narrowing level 2",
		"| go | units | 2 | 40 |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}

	var back struct {
		Lists struct {
			CannotTell map[string][]recording.Mark `json:"cannot_tell"`
		} `json:"lists"`
	}

	if err := json.Unmarshal(data, &back); err != nil || !back.Lists.CannotTell["e2e/e2e.a"][0].Since.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("marks: %+v %v", back, err)
	}
}
