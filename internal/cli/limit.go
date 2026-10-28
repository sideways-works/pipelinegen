package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

type stepLimit struct {
	key     string
	minutes int
	end     time.Time
}

// limitMargin leaves time to say which limit was reached and to free what the command holds before the
// CI system kills the step.
const limitMargin = plugin.LimitMarginSeconds * time.Second

// stepLimitFromEnv reads plugin.EnvLimit ("<key>=<minutes>"), counted from start unless
// plugin.EnvDeadline gives the end. A value it can't read is left out: the CI system's limit still holds.
func stepLimitFromEnv(start time.Time) (stepLimit, bool) {
	raw := os.Getenv(plugin.EnvLimit)
	if raw == "" {
		return stepLimit{}, false
	}

	key, m, _ := strings.Cut(raw, "=")

	minutes, err := strconv.Atoi(m)
	if err != nil || minutes < 1 || key == "" {
		warnf(os.Stderr, "pipelinegen: %s=%q is not <key>=<minutes>: the step's own limit is left to the CI system\n", plugin.EnvLimit, raw)

		return stepLimit{}, false
	}

	l := stepLimit{key: key, minutes: minutes, end: start.Add(time.Duration(minutes) * time.Minute)}

	if d := os.Getenv(plugin.EnvDeadline); d != "" {
		secs, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			warnf(os.Stderr, "pipelinegen: %s=%q is not Unix seconds: the limit counts from the command's start\n", plugin.EnvDeadline, d)
		} else {
			l.end = time.Unix(secs, 0)
		}
	}

	return l, true
}

func withStepLimit(ctx context.Context, l stepLimit) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, l.end.Add(-limitMargin))
}

func (l stepLimit) stopped(command string) string {
	return fmt.Sprintf("pipelinegen %s: stopped at its step's time limit of %s (%s), %s before the step would be killed, so it could say so and free what it held: raise %s if the step needs longer\n",
		command, minutes(time.Duration(l.minutes)*time.Minute), l.key, minutes(limitMargin), l.key)
}

func runLimited(ctx context.Context, name string, run func(context.Context) error, stderr io.Writer) error {
	limit, ok := stepLimitFromEnv(time.Now())
	if !ok {
		return run(ctx)
	}

	cctx, cancel := withStepLimit(ctx, limit)
	defer cancel()

	err := run(cctx)
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		warnf(stderr, "%s", limit.stopped(name))
	}

	return err
}
