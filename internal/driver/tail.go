package driver

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// tailWriter passes what a driver writes to the batch's log a line at a time under its name, redacted,
// and keeps the last lines for a slice the driver left without a result.
type tailWriter struct {
	mu     sync.Mutex
	out    io.Writer
	prefix string
	part   []byte
	lines  []string
}

func newTailWriter(out io.Writer, prefix string) *tailWriter {
	return &tailWriter{out: out, prefix: prefix}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.part = append(t.part, p...)

	for {
		i := bytes.IndexByte(t.part, '\n')
		if i < 0 {
			break
		}

		t.line(string(t.part[:i]))
		t.part = t.part[i+1:]
	}

	return len(p), nil
}

func (t *tailWriter) line(s string) {
	s = secret.Redact(strings.TrimRight(s, "\r"))

	t.lines = append(t.lines, s)
	if len(t.lines) > tailLines {
		t.lines = t.lines[len(t.lines)-tailLines:]
	}

	sayf(t.out, "%s%s\n", t.prefix, s)
}

func (t *tailWriter) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.part) > 0 {
		t.line(string(t.part))
		t.part = nil
	}
}

func (t *tailWriter) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.Join(t.lines, "\n")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return secret.Redact(strings.Join(lines, "\n"))
}
