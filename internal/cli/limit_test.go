package cli

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAStepEndsBeforeItsTimeLimitAndSaysWhich(t *testing.T) {
	t.Setenv(plugin.EnvLimit, "limits.steps=20")
	// the script stamped the step's end: the margin from now on is a second.
	t.Setenv(plugin.EnvDeadline, strconv.FormatInt(time.Now().Add(limitMargin+time.Second).Unix(), 10))

	start := time.Now()

	var stderr strings.Builder

	err := runLimited(t.Context(), "batches", func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "pipelinegen batches: stopped at its step's time limit of 20 minutes (limits.steps), 2 minutes before the step would be killed") ||
		!strings.Contains(stderr.String(), "raise limits.steps") {
		t.Fatalf("a step at its limit: %v %s", err, stderr.String())
	}

	if took := time.Since(start); took > time.Minute {
		t.Fatalf("it took %s", took)
	}

	for _, bad := range []string{"twenty", "limits.steps=0", "=5", "limits.steps=x"} {
		t.Setenv(plugin.EnvLimit, bad)

		if _, ok := stepLimitFromEnv(time.Now()); ok {
			t.Errorf("%q read as a limit", bad)
		}
	}

	t.Setenv(plugin.EnvLimit, "limits.plan=60")
	t.Setenv(plugin.EnvDeadline, "")

	now := time.Now()
	if l, ok := stepLimitFromEnv(now); !ok || l.key != "limits.plan" || !l.end.Equal(now.Add(time.Hour)) {
		t.Errorf("from the start: %+v %v", l, ok)
	}
}
