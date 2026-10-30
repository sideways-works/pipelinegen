package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAStepEndsBeforeItsTimeLimitAndSaysWhich(t *testing.T) {
	root, _, _ := runEnv(t)

	if _, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root); code != 0 {
		t.Fatalf("acquire: %d %s", code, stderr)
	}

	t.Setenv(envBatch, "2")
	t.Setenv(plugin.EnvLimit, "limits.steps=20")
	// the script stamped the step's end: the margin from now on is a second.
	t.Setenv(plugin.EnvDeadline, strconv.FormatInt(time.Now().Add(limitMargin+time.Second).Unix(), 10))

	start := time.Now()

	_, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root)
	if code != 1 || !strings.Contains(stderr, "pipelinegen lock: stopped at its step's time limit of 20 minutes (limits.steps), 2 minutes before the step would be killed") ||
		!strings.Contains(stderr, "raise limits.steps") {
		t.Fatalf("acquire at its limit: %d %s", code, stderr)
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
