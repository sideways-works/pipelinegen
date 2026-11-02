package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func declineWrites(t *testing.T, root string) {
	t.Helper()

	remote := filepath.Join(strings.TrimPrefix(os.Getenv(envGitURL), "file://"), "o", "r.git")

	hook := "#!/bin/sh\necho \"pre-receive hook declined\" >&2\nexit 1\n"
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(remote, "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(root, "pipelinegen.yaml")

	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(cfg, append(data, "writes: {retries: 0}\n"...), 0o600); err != nil { //nolint:gosec // the test's own config
		t.Fatal(err)
	}
}

func TestAStepThatCannotRecordFailsAsUnrecorded(t *testing.T) {
	root, _ := flushBatch(t)

	writeFile(t, "result/e2e.a.json", `{"passed": true, "failures": []}`)

	for _, step := range [][]string{{"start", "e2e.a"}, {"end", "e2e.a"}} {
		if _, stderr, code := run(t, append(append([]string{"slice"}, step...), "-root", root)...); code != 0 {
			t.Fatalf("slice %v: %d %s", step, code, stderr)
		}
	}

	declineWrites(t, root)

	_, stderr, code := run(t, "report", "batch-end", "--batch", "1", "-root", root)
	if code != ExitUnrecorded || !strings.Contains(stderr, "1 of 2 slices passed by their results") || !strings.Contains(stderr, "rerun the build") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	_, stderr, code = run(t, "report", "suite", "-root", root)
	if code != ExitUnrecorded || !strings.Contains(stderr, "the suite's end mark") {
		t.Fatalf("suite: %d %s", code, stderr)
	}
}
