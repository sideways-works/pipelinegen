package report

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestARunWithNoHostLivesInTheStore(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)

	cfg := &config.Config{
		Git: config.Git{Remote: remote},
		Runs: map[string]config.Run{"main": {
			Trigger: config.Trigger{Branch: "main"}, Compare: config.CompareLastPassed, Select: config.SelectAffected,
			Suites: map[string]config.RunSuite{"lint": {}},
		}},
		Suites: map[string]config.Suite{"lint": {}},
	}

	newRun := func(commit string) *Reporter {
		return &Reporter{Config: cfg, ID: ID{Run: "main", Change: "main", Commit: commit}, Store: testStore(t, remote), GitHub: Nowhere{}, Throttle: -1}
	}

	plan := `{"suites": [{"suite": "lint", "selected": [{"label": "//a:lint"}, {"label": "//b:lint"}], "untouched": [], "whole_suite": []}]}`

	state := func(commit string) string {
		t.Helper()

		got, err := RunState(ctx, testStore(t, remote), nil, ID{Run: "main", Change: "main", Commit: commit})
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := state(commit); got != "" {
		t.Fatalf("never planned: %q", got)
	}

	passing, failing := newRun(commit), newRun("89abcdef0123456789abcdef0123456789abcdef")

	for _, r := range []*Reporter{passing, failing} {
		if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
			t.Fatal(err)
		}

		if err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if got := state(commit); got != "pending" {
		t.Fatalf("planned: %q", got)
	}

	for slice, results := range map[string][2]string{"a.lint": {`{"passed": true}`, `{"passed": true}`}, "b.lint": {`{"passed": true}`, `{"passed": false}`}} {
		for i, r := range []*Reporter{passing, failing} {
			label := "//" + slice[:1] + ":lint"
			if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: slice, Label: label, Result: writeResult(t, results[i])}); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, r := range []*Reporter{passing, failing} {
		if _, err := r.Suite(ctx, "lint"); err != nil {
			t.Fatal(err)
		}

		if _, err := r.Finish(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if got := state(commit); got != "success" {
		t.Fatalf("passed: %q", got)
	}

	if got := state(failing.ID.Commit); got != "failure" {
		t.Fatalf("failed: %q", got)
	}
}
