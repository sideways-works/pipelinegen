package cli

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func flushBatch(t *testing.T) (string, *githubAPI) {
	t.Helper()

	root, api, s := runEnv(t)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}]}]}`
	if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)

	if _, stderr, code := run(t, "slice", "setup-end", "-root", root); code != 0 {
		t.Fatalf("setup-end: %d %s", code, stderr)
	}

	return root, api
}

func TestASlicesStartAndEndNeverReachTheNetwork(t *testing.T) {
	root, _ := flushBatch(t)

	t.Setenv(envGitURL, "http://127.0.0.1:1/")
	t.Setenv(envAPIURL, "http://127.0.0.1:1")

	writeFile(t, "result/e2e.a.json", `{"passed": false, "failures": [{"test": "pays"}]}`)

	for _, step := range [][]string{{"start", "e2e.a"}, {"end", "e2e.a"}, {"start", "e2e.b"}} {
		began := time.Now()

		_, stderr, code := run(t, append(append([]string{"slice"}, step...), "-root", root)...)
		if code != 0 || strings.Contains(stderr, "flush") || time.Since(began) > 10*time.Second {
			t.Fatalf("slice %v: %d in %s: %s", step, code, time.Since(began), stderr)
		}
	}
}

func TestTheBackgroundFlushShowsTheBatchAndMayDie(t *testing.T) {
	root, api := flushBatch(t)

	flusher := func() (*exec.Cmd, *bytes.Buffer) {
		t.Helper()

		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(t.Context(), exe, "slice", "flush", "--every", "100ms", "-root", root)

		cmd.Env = append(os.Environ(), envTestCommand+"=1")

		var out bytes.Buffer

		cmd.Stdout = &out

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		return cmd, &out
	}

	cmd, out := flusher()

	writeFile(t, "result/e2e.a.json", `{"passed": false, "failures": [{"test": "pays"}]}`)

	for _, step := range [][]string{{"start", "e2e.a"}, {"end", "e2e.a"}} {
		if _, stderr, code := run(t, append(append([]string{"slice"}, step...), "-root", root)...); code != 0 {
			t.Fatalf("slice %v: %d %s", step, code, stderr)
		}
	}

	for deadline := time.Now().Add(20 * time.Second); !strings.Contains(api.comment(7), "pays"); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the background flush never showed the failed slice:\n%s", api.comment(7))
		}
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err != nil || !strings.Contains(out.String(), "flushing stopped") {
		t.Fatalf("stopped: %v %q", err, out.String())
	}

	cmd, _ = flusher()
	_ = cmd.Process.Kill() //nolint:errcheck // the test's own process
	_ = cmd.Wait()         //nolint:errcheck // killed

	writeFile(t, "result/e2e.b.json", `{"passed": true}`)

	for _, step := range [][]string{{"start", "e2e.b"}, {"end", "e2e.b"}} {
		if _, stderr, code := run(t, append(append([]string{"slice"}, step...), "-root", root)...); code != 0 {
			t.Fatalf("slice %v: %d %s", step, code, stderr)
		}
	}

	if _, stderr, code := run(t, "report", "batch-end", "-root", root); code != 1 || !strings.Contains(stderr, "e2e.a failed") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if body := api.comment(7); !strings.Contains(body, "e2e.b") {
		t.Fatalf("the batch's end didn't write e2e.b:\n%s", body)
	}
}
