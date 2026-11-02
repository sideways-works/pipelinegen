package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestAJobRefusesToRunWithoutItsPlan(t *testing.T) {
	remote := storetest.Bare(t)
	store := storetest.Open(t, remote)

	id := report.ID{Run: "nightly", Change: "replay2", Commit: "0123456789abcdef0123456789abcdef01234567"}
	r := &report.Reporter{ID: id, Store: store}
	result := t.TempDir()

	err := checkPlanOnce(t.Context(), r, result)
	if !errors.Is(err, report.ErrNoPlan) || !strings.Contains(err.Error(), "run nightly (PIPELINEGEN_RUN), change replay2 (PIPELINEGEN_CHANGE), commit 0123456789ab (PIPELINEGEN_COMMIT)") {
		t.Fatalf("no plan: %v", err)
	}

	if _, err := store.Write(t.Context(), "plan", gitstore.Update{Ref: id.PlanRef(), Files: map[string][]byte{"plan.json": []byte("{}")}}); err != nil {
		t.Fatal(err)
	}

	if err := checkPlanOnce(t.Context(), r, result); err != nil {
		t.Fatalf("with its plan: %v", err)
	}

	r.Store = storetest.Open(t, filepath.Join(t.TempDir(), "gone.git"))
	if err := checkPlanOnce(t.Context(), r, result); err != nil {
		t.Fatalf("a later step read again: %v", err)
	}

	if err := os.Remove(filepath.Join(result, planChecked)); err != nil {
		t.Fatal(err)
	}

	err = checkPlanOnce(t.Context(), r, result)
	if err == nil || errors.Is(err, report.ErrNoPlan) || !strings.Contains(err.Error(), "the run's plans can't be read") {
		t.Fatalf("an unreadable store: %v", err)
	}
}
