package progress

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p) //nolint:wrapcheck // the buffer's own answer
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

func TestAStepSaysWhereItIsOnceItRunsLong(t *testing.T) {
	every = 50 * time.Millisecond

	t.Cleanup(func() { every = time.Minute })

	var out syncBuffer

	ctx, stop := Start(t.Context(), &out, "pipelinegen plan")
	Stage(ctx, "reading the lists")

	restore := Stage(ctx, "the plugins on %s", "abc")
	Count(ctx, 5, 5, "plugins")
	restore()

	Stage(ctx, "selecting the slices")

	if got := from(ctx).line(); !strings.HasPrefix(got, "pipelinegen plan: selecting the slices, 0s so far") {
		t.Fatalf("after a stage within another: %q", got)
	}

	defer Stage(ctx, "plugin %s: %s", "go-sbom", "units")()

	Count(ctx, 2, 5, "plugins")

	if got := out.String(); got != "" {
		t.Fatalf("said at once: %q", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "\n") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	stop()

	said := out.String()
	first, _, _ := strings.Cut(said, "\n")

	if !strings.HasPrefix(first, "pipelinegen plan: plugin go-sbom: units, 2 of 5 plugins done, ") || !strings.HasSuffix(first, " so far") {
		t.Fatalf("line %q", first)
	}

	time.Sleep(3 * every)

	if out.String() != said {
		t.Error("said more after its stop")
	}
}

func TestNoMeterNoWords(_ *testing.T) {
	Stage(context.Background(), "x")()
	Count(context.Background(), 1, 2, "y")
}
