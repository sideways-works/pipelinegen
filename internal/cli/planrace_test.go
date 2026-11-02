package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestAPlannerThatLostTheRaceStopsAsPlannedAlready(t *testing.T) {
	plannerLostTheRace(t)
}

func TestAPlannerThatLostTheRaceStopsAsPlannedAlreadyOnABranch(t *testing.T) {
	onBranch(t, plannerLostTheRace)
}

func plannerLostTheRace(t *testing.T) {
	t.Helper()

	root, _, _ := runEnv(t)

	cfg, err := config.Load(filepath.Join(root, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ch := runs.Change{Run: "pr", ID: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567", PR: 7}
	heads := []string{"head-a", "head-b"}
	outs := make([]bytes.Buffer, len(heads))
	wrote := make([]bool, len(heads))
	errs := make([]error, len(heads))

	var wg sync.WaitGroup

	for i, head := range heads {
		store := openStore(cfg)

		t.Cleanup(func() { closeOrFail(t, store) })

		r := &report.Reporter{Config: cfg, ID: reportID(ch), Store: store, GitHub: host(cfg), Warn: io.Discard}
		plan := &affected.Plan{Head: head, Suites: []affected.SuitePlan{{Suite: "e2e"}}}

		wg.Go(func() {
			err := startRun(t.Context(), store, r, plan, nil)
			if err == nil {
				wrote[i] = true

				return
			}

			errs[i] = plannedAlready(err, ch, &outs[i])
		})
	}

	wg.Wait()

	winner := -1

	for i := range heads {
		switch {
		case errs[i] != nil:
			t.Fatalf("planner %d: %v", i, errs[i])
		case wrote[i] && winner >= 0:
			t.Fatalf("planners %d and %d both wrote the plan", winner, i)
		case wrote[i]:
			winner = i
		case outs[i].String() != "pr pr-7: planned already\n":
			t.Fatalf("the planner that lost said %q", outs[i].String())
		}
	}

	if winner < 0 {
		t.Fatal("no planner wrote the plan")
	}

	reader := openStore(cfg)

	t.Cleanup(func() { closeOrFail(t, reader) })

	plan, ok, err := readPlan(t.Context(), reader, reportID(ch))
	if err != nil || !ok {
		t.Fatalf("plan: %v %v", ok, err)
	}

	if plan.Head != heads[winner] {
		t.Fatalf("the plan's head is %s, the writer's was %s", plan.Head, heads[winner])
	}
}

func closeOrFail(t *testing.T, s *gitstore.Store) {
	t.Helper()

	if err := s.Close(); err != nil {
		t.Error(err)
	}
}
