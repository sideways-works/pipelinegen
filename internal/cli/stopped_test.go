package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestAStoppedBatchSaysWhetherItsTimeLimitStoppedIt(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		suiteAgo     time.Duration
		batchAgo     time.Duration
		want         string
	}{
		{"surely not the limit", reportConfig, 10 * time.Minute, 0,
			"batch 1 stopped while slice e2e.a ran, 10 minutes after the suite began: not its time limit of 90 minutes (suites.e2e.minutes); its task died"},
		{"maybe the limit", reportConfig, 130 * time.Minute, 0,
			"batch 1 stopped while slice e2e.a ran; its time limit is 90 minutes (suites.e2e.minutes): the build log says `timeout exceeded` when that was the cause; raise suites.e2e.minutes"},
		{"an older pipeline's batch stamp, at its limit", reportConfig, 200 * time.Minute, 91 * time.Minute,
			"batch 1 stopped while slice e2e.a ran after 91 minutes: its time limit of 90 minutes (suites.e2e.minutes) stopped it"},
		{"an older pipeline's batch stamp, within it", reportConfig, 200 * time.Minute, 30 * time.Minute,
			"batch 1 stopped while slice e2e.a ran after 30 minutes, within its time limit of 90 minutes (suites.e2e.minutes): its task died"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, api, s := runEnv(t)
			writeFile(t, filepath.Join(root, config.FileName), tc.config)

			id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

			plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}, {"label": "//e2e:c"}], "whole_suite": []}]}`
			if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
				t.Fatal(err)
			}

			t.Chdir(t.TempDir())
			writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
			writeFile(t, "batches/batches.json", `["1"]`)

			if err := writeStarted("batches", time.Now().Add(-tc.suiteAgo)); err != nil {
				t.Fatal(err)
			}

			args := []string{"report", "batch-end", "-root", root}

			if tc.batchAgo > 0 {
				if err := writeStarted("slot", time.Now().Add(-tc.batchAgo)); err != nil {
					t.Fatal(err)
				}

				args = append(args, "-started", "slot")
			}

			if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 0 {
				t.Fatalf("slice start: %d %s", code, stderr)
			}

			_, stderr, code := run(t, args...)
			if code != 1 || !strings.Contains(stderr, "e2e.a errored, e2e.b errored") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("batch-end: %d %s\nwant %s", code, stderr, tc.want)
			}

			if body := api.comment(7); !strings.Contains(body, tc.want) {
				t.Fatalf("the comment doesn't say why:\n%s", body)
			}

			if _, _, code := run(t, "report", "suite", "-root", root, "-batches", "batches"); code != 1 {
				t.Fatalf("suite: %d", code)
			}

			if got := api.status("pipelinegen/pr/e2e"); !strings.HasPrefix(got, "error ") {
				t.Fatalf("suite status %q", got)
			}

			if body := api.comment(7); strings.Contains(body, "never reported how it ended") || !strings.Contains(body, "suite e2e") {
				t.Fatalf("the comment doesn't say why the suite stopped:\n%s", body)
			}
		})
	}
}

func TestASuiteWhoseWorkNeverBeganSaysSo(t *testing.T) {
	root, api, s := runEnv(t)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())

	if _, _, code := run(t, "report", "suite", "-root", root, "-batches", "batches"); code != 1 {
		t.Fatalf("suite: %d", code)
	}

	if body := api.comment(7); !strings.Contains(body, "suite e2e's work never began: its start step") || !strings.Contains(body, "(limits.steps)") {
		t.Fatalf("the comment:\n%s", body)
	}
}
