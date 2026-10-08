// Package progress says on stderr where a long step is: once a minute after
// the first, which stage it is in, how far it got and how long it has run,
// so a slow step can be told from a stuck one.
package progress

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

var every = time.Minute

// Meter is what one step says about itself.
type Meter struct {
	mu     sync.Mutex
	prefix string
	out    io.Writer
	start  time.Time
	stage  string
	count  string
}

type key struct{}

// Start begins saying where the step is, on out, each line starting with
// prefix ("pipelinegen plan"). The returned context carries the meter for
// Stage and Count; stop ends it, and the step says nothing after.
func Start(ctx context.Context, out io.Writer, prefix string) (context.Context, func()) {
	m := &Meter{prefix: prefix, out: out, start: time.Now()}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		tick := time.NewTicker(every)
		defer tick.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.say()
			}
		}
	}()

	return context.WithValue(ctx, key{}, m), func() {
		cancel()
		<-done
	}
}

func from(ctx context.Context) *Meter {
	m, _ := ctx.Value(key{}).(*Meter) //nolint:errcheck // a context without a meter says nothing
	return m
}

// Stage says which stage the step is in now ("reading the lists",
// "bazel-diff generate-hashes", "plugin go-sbom: units"), and returns what
// puts the stage before back, with its count, for a stage within another:
// defer it. A count stays through the stages within its own.
func Stage(ctx context.Context, format string, args ...any) func() {
	m := from(ctx)
	if m == nil {
		return func() {}
	}

	m.mu.Lock()
	stage, count := m.stage, m.count
	m.stage = fmt.Sprintf(format, args...)
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		m.stage, m.count = stage, count
		m.mu.Unlock()
	}
}

// Count says how far the step got, where it can tell: done of total
// things ("2 of 5 plugins"); a total of 0 clears it.
func Count(ctx context.Context, done, total int, things string) {
	if m := from(ctx); m != nil {
		m.mu.Lock()

		m.count = ""
		if total > 0 {
			m.count = fmt.Sprintf("%d of %d %s", done, total, things)
		}

		m.mu.Unlock()
	}
}

func (m *Meter) say() {
	_, _ = io.WriteString(m.out, m.line()) //nolint:errcheck // a line that can't be said changes nothing
}

func (m *Meter) line() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	line := m.prefix + ": "
	if m.stage != "" {
		line += m.stage + ", "
	}

	if m.count != "" {
		line += m.count + " done, "
	}

	return line + fmt.Sprintf("%s so far\n", time.Since(m.start).Round(time.Second))
}
