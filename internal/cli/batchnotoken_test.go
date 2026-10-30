package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestABatchWithoutTheStoreCredentialReadsNoStore(t *testing.T) {
	root, _, _ := runEnv(t)

	t.Setenv(envToken, "")
	t.Setenv(envGitURL, "http://127.0.0.1:1/")
	t.Setenv(envAPIURL, "http://127.0.0.1:1")

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	steps := [][]string{{"setup-end"}, {"start", "e2e.a"}, {"end", "e2e.a"}}

	writeFile(t, "result/e2e.a.json", `{"passed": true}`)

	for _, step := range steps {
		if _, stderr, code := run(t, append(append([]string{"slice"}, step...), "-root", root)...); code != 0 {
			t.Fatalf("slice %v without the store's credential: %d %s", step, code, stderr)
		}
	}

	if _, err := os.Stat(filepath.Join("result", report.JournalDir)); err != nil {
		t.Fatalf("the batch's journal: %v", err)
	}
}

func TestTheBatchesStepRefusesARunWithoutItsPlan(t *testing.T) {
	runEnv(t)

	r := testrepo.New(t)
	r.Write("pipelinegen.yaml", branchConfig)
	r.Commit("config")

	_, stderr, code := run(t, "batches", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", filepath.Join(t.TempDir(), "batches"), "-root", r.Dir)
	if code == 0 || !strings.Contains(stderr, errNoPlan.Error()) {
		t.Fatalf("batches without a plan: %d %s", code, stderr)
	}
}
