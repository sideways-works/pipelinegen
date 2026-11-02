package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

const goldenCommit = "0123456789abcdef0123456789abcdef01234567"

func TestStoredNamesStayAsTheyAre(t *testing.T) {
	pr := headID("review", runs.Head{PR: 7, Commit: goldenCommit, Branch: "feature/cart"})
	branch := report.ID{Run: "nightly", Change: "main", Commit: goldenCommit}

	got := map[string]string{
		"pr id":         pr.Run + " " + pr.Change + " " + pr.Commit,
		"pr records":    pr.Prefix(),
		"pr state":      pr.StateRef("e2e", "e2e/cart.spec.ts"),
		"pr end":        pr.EndRef("e2e"),
		"pr plan":       pr.PlanRef(),
		"pr plans":      report.PlansOfChange(pr.Run, pr.Change),
		"pr mark":       report.SweptRef(pr),
		"pr also-to":    report.AlsoToRef(pr.Run, pr.Change),
		"main records":  branch.Prefix(),
		"main state":    branch.StateRef("go", "app/price_test.go"),
		"main plan":     branch.PlanRef(),
		"main mark":     report.SweptRef(branch),
		"runs of":       report.RunsOf("review"),
		"lists":         lists.Ref(goldenCommit),
		"attached":      attached.Ref(goldenCommit),
		"run (grouped)": gitstore.RunPrefix + strings.TrimSuffix(strings.TrimPrefix(pr.Prefix(), "refs/pipelinegen/runs/"), "/"),
		"batch prefix":  gitstore.BatchPrefix,
	}

	want := map[string]string{
		"pr id":         "review pr-7 " + goldenCommit,
		"pr records":    "refs/pipelinegen/runs/review/pr-7/0123456789ab/",
		"pr state":      "refs/pipelinegen/runs/review/pr-7/0123456789ab/e2e/e2e/cart.spec.ts/=",
		"pr end":        "refs/pipelinegen/runs/review/pr-7/0123456789ab/_end/e2e",
		"pr plan":       "refs/pipelinegen/plans/review/pr-7/0123456789ab",
		"pr plans":      "refs/pipelinegen/plans/review/pr-7/",
		"pr mark":       "refs/pipelinegen/swept/review/pr-7/0123456789ab",
		"pr also-to":    "refs/pipelinegen/also-to/review/pr-7",
		"main records":  "refs/pipelinegen/runs/nightly/main/0123456789ab/",
		"main state":    "refs/pipelinegen/runs/nightly/main/0123456789ab/go/app/price_test.go/=",
		"main plan":     "refs/pipelinegen/plans/nightly/main/0123456789ab",
		"main mark":     "refs/pipelinegen/swept/nightly/main/0123456789ab",
		"runs of":       "refs/pipelinegen/runs/review/",
		"lists":         "refs/pipelinegen/lists/" + goldenCommit,
		"attached":      "refs/pipelinegen/attached/" + goldenCommit,
		"run (grouped)": "refs/pipelinegen/run/review/pr-7/0123456789ab",
		"batch prefix":  "refs/pipelinegen/batch/",
	}

	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %q, want %q", k, got[k], w)
		}
	}

	dir := t.TempDir()
	if err := (runs.Head{PR: 7, Commit: goldenCommit, Branch: "feature/cart", Updated: time.Unix(0, 0)}).Files(dir); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"version.json":  `{"commit":"` + goldenCommit + `","pr":"7"}`,
		"metadata.json": `[{"name":"pr","value":"7"},{"name":"head_name","value":"feature/cart"},{"name":"head_sha","value":"` + goldenCommit + `"}]`,
		"head_name":     "feature/cart",
	}

	for name, w := range files {
		data, err := os.ReadFile(filepath.Join(dir, ".git", "resource", name))
		if err != nil || string(data) != w {
			t.Errorf("%s: %q (%v), want %q", name, data, err, w)
		}
	}

	cfg := &config.Config{Runs: map[string]config.Run{"review": {Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}}}}

	ch, err := runs.FromTrigger(t.Context(), cfg, "review", dir)
	if err != nil || ch != (runs.Change{Run: "review", ID: "pr-7", Commit: goldenCommit, PR: 7, Branch: "feature/cart", Base: "main"}) {
		t.Fatalf("from the hand-off: %+v %v", ch, err)
	}
}

func TestAConnectorsChangeIsOneRefPart(t *testing.T) {
	h := runs.Head{Change: "git/branch-review%2Fcart", Commit: goldenCommit, Branch: "review/cart"}
	id := headID("review", h)

	if id.Change != h.Change || id.PR != 0 || h.Label() != "git/branch-review%2Fcart" {
		t.Fatalf("id %+v, label %q", id, h.Label())
	}

	if got := id.PlanRef(); got != "refs/pipelinegen/plans/review/git%2Fbranch-review%252Fcart/0123456789ab" {
		t.Fatalf("plan ref %q", got)
	}

	part := strings.Split(strings.TrimPrefix(id.PlanRef(), report.AllPlans), "/")[1]
	if back, err := report.NameFromRef(part); err != nil || back != h.Change {
		t.Fatalf("read back %q %v", back, err)
	}

	if got := (runs.Head{PR: 7}).Label(); got != "#7" {
		t.Fatalf("a pull request's label %q", got)
	}
}
