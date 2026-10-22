package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

func TestAFailedPlanLeavesNoStatusPending(t *testing.T) {
	const token = "moved-tree-leaked-0123456789abcdef"

	secret.Add(token)

	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")

	if err := gh.SetStatus(t.Context(), commit, CommitStatus{State: statusPending, Context: Context(r.Config, "pr", "e2e")}); err != nil {
		t.Fatal(err)
	}

	cause := fmt.Errorf("plan: %w: abc was checked out to hash, def was there after (%s)", affected.ErrMoved, token)
	if err := r.PlanFailed(t.Context(), cause); err != nil {
		t.Fatal(err)
	}

	contexts := []string{RunContext(r.Config, "pr")}
	for name := range r.runConfig().Suites {
		contexts = append(contexts, Context(r.Config, "pr", name))
	}

	for _, c := range contexts {
		st, ok := gh.status(commit, c)
		if !ok || st.State != statusError || !strings.HasPrefix(st.Description, "planning failed: plan: the work tree changed underneath the plan") ||
			strings.Contains(st.Description, token[:12]) || len(st.Description) > maxDescription {
			t.Errorf("%s: %+v", c, st)
		}
	}

	for _, pr := range []int{7, 8} {
		body := onlyBody(t, gh, pr)
		if !strings.Contains(body, "planning failed, so no slice ran") || !strings.Contains(body, "the work tree changed underneath the plan") ||
			strings.Contains(body, token) {
			t.Errorf("#%d:\n%s", pr, body)
		}
	}
}
