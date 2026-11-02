package cli

import (
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestAProposedChangesPlanWritesNothingShared(t *testing.T) {
	t.Setenv(envJobURL, "https://ci/jobs/plan/1")

	cfg := &config.Config{
		Concourse: config.Concourse{Branch: "main"},
		Runs: map[string]config.Run{
			"pr":   {Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}},
			"main": {Trigger: config.Trigger{Branch: "main"}},
		},
	}

	for _, tc := range []struct {
		run, change string
		pr          int
		trusted     bool
	}{
		{"pr", "pr-4", 4, false},
		{"main", "main", 0, true},
		{"main", "feature", 0, false},
	} {
		proposed := tc.pr != 0 || !trustedRun(cfg, tc.run, tc.change)
		if proposed == tc.trusted {
			t.Errorf("%s %s: proposed %v", tc.run, tc.change, proposed)
		}

		store := &gitstore.Store{Remote: "file:///nowhere", Proposed: proposed}
		if got := answersCache(t.TempDir(), store).Push; got != tc.trusted {
			t.Errorf("%s %s: the plan writes answers: %v", tc.run, tc.change, got)
		}
	}

	t.Setenv(envJobURL, "")
	t.Setenv(envPipelineStep, "1")

	for _, proposed := range []bool{false, true} {
		store := &gitstore.Store{Remote: "file:///nowhere", Proposed: proposed}
		if answersCache(t.TempDir(), store).Push {
			t.Errorf("a planning step pushes answers, proposed %v", proposed)
		}
	}

	t.Setenv(envJobURL, "https://ci/jobs/plan/1")

	cfg.Keep = config.Keep{Runs: config.Retention{For: "7d"}, Lists: config.ListsRetention{Retention: config.Retention{For: "90d"}}}
	remote := storetest.Bare(t)
	s := &gitstore.Store{Remote: remote, Clock: func() time.Time { return time.Now().AddDate(0, 0, -30) }}

	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // a scratch repository

	old := report.ID{Run: "pr", Change: "pr-1", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, old, []byte(`{"suites": []}`)); err != nil {
		t.Fatal(err)
	}

	proposed := &gitstore.Store{Remote: remote, Proposed: true}

	t.Cleanup(func() { _ = proposed.Close() }) //nolint:errcheck // a scratch repository

	sweepStore(t.Context(), cfg, runs.Change{Run: "pr", ID: "pr-4", PR: 4}, proposed, t.TempDir(), &affected.Plan{})

	if _, ok, err := proposed.First(t.Context(), []string{old.PlanRef()}); err != nil || !ok {
		t.Errorf("a proposed change's plan swept a run: %v %v", ok, err)
	}

	trusted := &gitstore.Store{Remote: remote}

	t.Cleanup(func() { _ = trusted.Close() }) //nolint:errcheck // a scratch repository

	sweepStore(t.Context(), cfg, runs.Change{Run: "main", ID: "main"}, trusted, t.TempDir(), &affected.Plan{})

	if _, ok, err := trusted.First(t.Context(), []string{old.PlanRef()}); err != nil || ok {
		t.Errorf("the default branch's plan kept a month-old run: %v %v", ok, err)
	}
}
