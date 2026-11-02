package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func wholeStep(t *testing.T, root, check, result string) (string, int) {
	t.Helper()

	build := t.TempDir()
	t.Chdir(build)

	if err := os.Symlink(root, "source"); err != nil {
		t.Fatal(err)
	}

	if result != "" {
		writeFile(t, filepath.Join("result-whole."+check, "result.json"), result)
	}

	writeFile(t, "trigger/.git/resource/version.json", `{"pr": "7", "commit": "0123456789abcdef0123456789abcdef01234567"}`)
	writeFile(t, "trigger/.git/resource/head_name", "feature\n")

	for k, v := range map[string]string{"PIPELINEGEN_BAZELISK": "tools/bazelisk", envTrigger: "trigger", envChange: "", envCommit: "", envPR: ""} {
		t.Setenv(k, v)
	}

	_, stderr, code := run(t, "report", "whole", "--name", check, "--result", "result-whole."+check, "--root", "source")

	return stderr, code
}

func wholeRun(t *testing.T) (string, *githubAPI, func() map[string]report.State) {
	t.Helper()

	root, api, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(reportConfig,
		"    lock: {name: browsers, minutes: 90, slots: 1}\n",
		"    lock: {name: browsers, minutes: 90, slots: 1}\n    whole_suite: [{name: reconcile, task: ci/tasks/reconcile.yml}]\n", 1))

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "whole_suite": ["reconcile"]}]}`

	if err := report.WritePlan(t.Context(), store, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	return root, api, func() map[string]report.State {
		t.Helper()

		got, err := report.States(t.Context(), store, id, "e2e")
		if err != nil {
			t.Fatal(err)
		}

		return got
	}
}

func TestAWholeSuiteCheckThatPassedReportsPassed(t *testing.T) {
	root, api, states := wholeRun(t)

	if stderr, code := wholeStep(t, root, "reconcile", `{"passed": true}`); code != 0 {
		t.Fatalf("whole: %d %s", code, stderr)
	}

	if got := states()["whole.reconcile"]; got != report.Passed {
		t.Fatalf("state %s", got)
	}

	if body := api.comment(7); !strings.Contains(body, "whole.reconcile") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestAWholeSuiteCheckThatFailedReportsItsFailures(t *testing.T) {
	root, api, states := wholeRun(t)

	stderr, code := wholeStep(t, root, "reconcile", `{"passed": false, "failures": [{"test": "ledger balances", "tail": "off by 3"}]}`)
	if code != 1 || !strings.Contains(stderr, "whole.reconcile failed") {
		t.Fatalf("whole: %d %s", code, stderr)
	}

	if got := states()["whole.reconcile"]; got != report.Failed {
		t.Fatalf("state %s", got)
	}

	if body := api.comment(7); !strings.Contains(body, "ledger balances") || !strings.Contains(body, "off by 3") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestAWholeSuiteCheckWithoutAResultErrors(t *testing.T) {
	root, api, states := wholeRun(t)

	stderr, code := wholeStep(t, root, "reconcile", "")
	if code != 1 || !strings.Contains(stderr, "whole.reconcile errored") {
		t.Fatalf("whole: %d %s", code, stderr)
	}

	if got := states()["whole.reconcile"]; got != report.Errored {
		t.Fatalf("state %s", got)
	}

	if body := api.comment(7); !strings.Contains(body, "the whole-suite check reconcile stopped before it wrote its result; its time limit is 90 minutes (suites.e2e.minutes)") {
		t.Fatalf("comment:\n%s", body)
	}

	if _, _, code := run(t, "report", "whole", "--result", "x", "--root", "source"); code != 2 {
		t.Fatalf("no name: %d", code)
	}
}
