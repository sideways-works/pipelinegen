package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestAttachJudgesEachSliceByItsOwnState(t *testing.T) {
	root, api, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), branchConfig+"    record: true\n")
	c := checkout(t, root, 2)

	t.Setenv(envRun, "main")
	t.Setenv(envChange, "main")
	t.Setenv(envPR, "0")

	plan := `{"all": true, "suites": [{"suite": "e2e", "all": true, "selected": [{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts"}]}]}`

	t.Setenv(envCommit, c[1])

	ended := report.ID{Run: "main", Change: "main", Commit: c[1]}
	if err := report.WritePlan(t.Context(), store, ended, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	recorded(t, store, ended, "e2e", "e2e/a.spec.ts", report.Passed, unitCreate+"\n")
	recorded(t, store, ended, "e2e", "e2e/b.spec.ts", report.Failed, unitRefund+"\n")

	if _, stderr, code := run(t, "report", "suite", "--root", root); code != 0 {
		t.Fatalf("suite: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/main"); !strings.HasPrefix(got, "success") {
		t.Fatalf("run status %q", got)
	}

	got, err := lists.Decode(listFiles(t, store, c[1]))
	if err != nil {
		t.Fatal(err)
	}

	if _, made := got["e2e/e2e/b.spec.ts"]; made || !slices.Equal(got["e2e/e2e/a.spec.ts"], []recording.ID{unitCreate}) {
		t.Fatalf("lists after the suite's end: %v", got)
	}

	attached := report.ID{Run: "main", Change: "main", Commit: c[2]}
	if err := report.WritePlan(t.Context(), store, attached, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	recorded(t, store, attached, "e2e", "e2e/a.spec.ts", report.Passed, unitCreate+"\n")
	recorded(t, store, attached, "e2e", "e2e/c.spec.ts", report.Passed, unitTotal+"\n")

	if _, stderr, code := run(t, "lists", "attach", "--root", root, "--run", "main", "--change", "main", "--commit", c[2], "--to", c[2]); code != 0 {
		t.Fatalf("attach: %d %s", code, stderr)
	}

	if got, err = lists.Decode(listFiles(t, store, c[2])); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got["e2e/e2e/c.spec.ts"], []recording.ID{unitTotal}) {
		t.Fatalf("lists after attach: %v", got)
	}
}
