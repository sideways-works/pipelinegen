package cli

import (
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestEachSuiteComparesFromItsOwnNewestPasses(t *testing.T) {
	commits := []string{"c6", "c5", "c4", "c3", "c2", "c1"}
	passes := map[string]report.Pass{
		// the checks passed on the newest commits, the browser suite's slices only further back
		"checks/lint":   {Commit: "c6"},
		"checks/vet":    {Commit: "c5"},
		"checks/format": {Commit: "c4"},
		"e2e/a":         {Commit: "c3"},
		"e2e/b":         {Commit: "c2"},
		"e2e/c":         {Commit: "c1"},
	}

	if got := newestPassed(commits, passes); !slices.Equal(got, []string{"c6", "c5", "c3", "c2"}) {
		t.Fatalf("compared from %v, want each suite's newest two", got)
	}

	// a suite each, at most twice passedSinceBases in all, newest first
	for i, s := range []string{"a", "b", "c", "d", "e", "f"} {
		passes[s+"/x"] = report.Pass{Commit: commits[i]}
	}

	if got := newestPassed(commits, passes); len(got) != passedSinceMost || got[0] != "c6" {
		t.Fatalf("compared from %v", got)
	}
}
