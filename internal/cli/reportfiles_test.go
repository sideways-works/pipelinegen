package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestReportFilesWritesAFailedSlicesFiles(t *testing.T) { reportFilesWrites(t) }

func TestReportFilesWritesAFailedSlicesFilesOnABranch(t *testing.T) { onBranch(t, reportFilesWrites) }

func reportFilesWrites(t *testing.T) {
	t.Helper()

	root, _, store := runEnv(t)
	commit := "0123456789abcdef0123456789abcdef01234567"

	if err := report.WritePlan(t.Context(), store, report.ID{Run: "pr", Change: "pr-7", Commit: commit}, []byte(keptPlan)); err != nil {
		t.Fatal(err)
	}

	result := t.TempDir()
	writeTestFile(t, filepath.Join(result, "result.json"),
		`{"passed": false, "failures": [{"test": "pays", "tail": "no"}], "files": [{"path": "shots/pay.png", "label": "screenshot"}, {"path": "../outside.txt"}]}`)
	writeTestFile(t, filepath.Join(result, "shots", "pay.png"), "png")

	t.Setenv(envSlice, "e2e.checkout")
	t.Setenv(envLabel, "//e2e:checkout")

	if _, stderr, code := run(t, "report", "slice", "--root", root, "--result", result); code != 1 {
		t.Fatalf("report slice: %d %s", code, stderr)
	}

	out := t.TempDir()

	stdout, stderr, code := run(t, "report", "files", "--root", root, "--run", "pr", "--change", "pr-7", "--out", out)
	if want := "e2e e2e.checkout (failed):\n  shots/pay.png (screenshot), 3 bytes\n  dropped ../outside.txt: not a plain path inside the result directory\n" +
		"wrote 1 files of pr pr-7 at 0123456789ab into " + out + "\n"; code != 0 || stdout != want {
		t.Fatalf("%d %s\n%s\nwant\n%s", code, stderr, stdout, want)
	}

	if data, err := os.ReadFile(filepath.Join(out, "e2e", "e2e.checkout", "shots", "pay.png")); err != nil || string(data) != "png" {
		t.Fatalf("written: %q %v", data, err)
	}

	stdout, stderr, code = run(t, "why", "--root", root, "--run", "pr", "--change", "pr-7", "--slice", "e2e.checkout")
	if want := "  failure files: 1 kept, 1 dropped; pipelinegen report files --run pr --change pr-7 --commit 0123456789ab --slice e2e.checkout --out failure-files writes them out"; code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("why: %d %s\n%s", code, stderr, stdout)
	}

	if stdout, _, code := run(t, "report", "files", "--root", root, "--run", "pr", "--change", "pr-7", "--slice", "e2e.cart"); code != 0 || !strings.HasPrefix(stdout, "no failed slice of pr pr-7") {
		t.Fatalf("a slice without files: %d %s", code, stdout)
	}

	if _, stderr, code := run(t, "report", "files", "--root", root, "--run", "pr"); code != 2 || !strings.Contains(stderr, "--run and --change") {
		t.Fatalf("no change: %d %s", code, stderr)
	}
}

func writeTestFile(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
