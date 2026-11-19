package progress

import (
	"bytes"
	"context"
	"io"
	"maps"
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

func TestTheSummarySaysWhereTheTimeWent(t *testing.T) {
	ctx, stop := Start(t.Context(), io.Discard, "pipelinegen tree")
	defer stop()

	m := from(ctx)
	base := m.start

	// stages and git commands at set times: the meter's clock is moved by hand
	at := func(d time.Duration) {
		m.mu.Lock()
		m.charge(base.Add(d))
		m.mu.Unlock()
	}

	m.mu.Lock()
	m.stage = "plugin go-sbom: units of 3 files"
	m.mu.Unlock()
	at(2 * time.Second)

	m.mu.Lock()
	m.stage = "plugin go-sbom: links of 3 files"
	m.mu.Unlock()
	at(3 * time.Second)

	m.mu.Lock()
	m.stage = "the store: reading"
	m.mu.Unlock()
	at(4500 * time.Millisecond)

	m.mu.Lock()
	m.stage = ""
	m.git["fetch"] = &calls{n: 3, d: 900 * time.Millisecond}
	m.git["push"] = &calls{n: 1, d: 600 * time.Millisecond}
	m.mu.Unlock()

	got := Summary(ctx)

	for _, want := range []string{
		"pipelinegen tree: took ",
		": plugin go-sbom 3.0s, the store 1.5s",
		"; git reached a remote 4 times in 1.5s (3 fetch, 1 push)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
}

func TestOnlyGitThatReachesARemoteIsCounted(t *testing.T) {
	ctx, stop := Start(t.Context(), io.Discard, "pipelinegen report batch-end")
	defer stop()

	for _, args := range [][]string{
		{"fetch", "--quiet", "origin"},
		{"-c", "protocol.version=2", "fetch", "origin", "x"},
		{"-C", "/tmp/push", "ls-remote", "origin"},
		{"push", "--atomic", "origin"},
		{"-C", "/tmp/fetch", "rev-parse", "HEAD"},
		{"cat-file", "--batch"},
		{"hash-object", "-w", "--stdin-paths"},
	} {
		Git(ctx, args)()
	}

	m := from(ctx)
	got := map[string]int{}

	for v, c := range m.git {
		got[v] = c.n
	}

	if want := map[string]int{"fetch": 2, "ls-remote": 1, "push": 1}; !maps.Equal(got, want) {
		t.Errorf("counted %v, want %v", got, want)
	}

	if !strings.HasSuffix(Summary(ctx), "; git reached a remote 4 times in 0.0s (2 fetch, 1 ls-remote, 1 push)\n") {
		t.Errorf("summary %q", Summary(ctx))
	}
}

func TestNoMeterNoSummary(t *testing.T) {
	Git(context.Background(), []string{"fetch"})()

	if got := Summary(context.Background()); got != "" {
		t.Errorf("said %q", got)
	}
}
