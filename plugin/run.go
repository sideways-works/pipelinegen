package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// ErrCantTell is a plugin's answer that it can't tell, for this tree or this slice.
var ErrCantTell = errors.New("can't tell")

var ErrFailed = errors.New("plugin failed")

type Plugin struct {
	Name    string
	Path    string
	Config  map[string]any
	Cache   string
	Timeout time.Duration
	// Caches are the cache names the plugin declared, each a directory under CachesDir (default
	// Cache), given to the plugin as its toolchain's variable.
	Caches []string
	// CachesDir is shared by every plugin that declares the same cache (a Go module cache is the
	// same for all).
	CachesDir string
	// Log takes, a line at a time under the plugin's name, what it wrote on stderr during a call
	// that answered; a failed call carries it in its error.
	Log io.Writer
	Dir string
}

func (p Plugin) cacheEnv() map[string]string {
	out := map[string]string{}

	dir := p.CachesDir
	if dir == "" {
		dir = p.Cache
	}

	if dir == "" {
		return out
	}

	for _, name := range p.Caches {
		if v, ok := secret.CacheVars[name]; ok {
			out[v] = filepath.Join(dir, name)
		}
	}

	return out
}

// Call sends req as JSON on stdin and decodes stdout into answer; exit code ExitCantTell is
// ErrCantTell.
func (p Plugin) Call(ctx context.Context, call string, req, answer any) error {
	if p.Timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	in, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", ErrFailed, p.Name, call, err)
	}

	cmd := exec.CommandContext(ctx, p.Path, call) //nolint:gosec // a plugin the config pins by checksum
	cmd.Stdin = bytes.NewReader(in)
	cmd.Dir = p.Dir
	// a plugin reads files: it gets no token and no network settings
	cmd.Env = secret.PluginEnv(p.cacheEnv())

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == ExitCantTell {
			// why it can't tell is part of the answer (a missing toolchain,
			// no evidence of a recording)
			return &cantTellError{plugin: p.Name, call: call, message: secret.Redact(tail(stderr.String(), 5))}
		}

		return fmt.Errorf("%w: %s %s: %w: %s", ErrFailed, p.Name, call, err, secret.Redact(tail(stderr.String(), 15)))
	}

	p.logStderr(call, stderr.String())

	if answer == nil {
		return nil
	}

	if err := json.Unmarshal(stdout.Bytes(), answer); err != nil {
		return fmt.Errorf("%w: %s %s: unreadable answer: %w", ErrFailed, p.Name, call, err)
	}

	return nil
}

func (p Plugin) NewRequest(work string) Request {
	return Request{Protocol: Protocol, Work: work, Cache: p.Cache, Config: p.Config}
}

func (p Plugin) Describe(ctx context.Context, work string) (Description, error) {
	var d Description
	if err := p.Call(ctx, CallDescribe, p.NewRequest(work), &d); err != nil {
		return d, err
	}

	if d.Protocol != Protocol {
		return d, fmt.Errorf("%w: %s speaks protocol %d, not %d", ErrFailed, p.Name, d.Protocol, Protocol)
	}

	return d, nil
}

// ReadLines appends to items the JSON Lines of T in an answer's files in the work directory.
func ReadLines[T any](work string, files []string, items []T) ([]T, error) {
	for _, f := range files {
		if filepath.IsAbs(f) || strings.Contains(f, "..") {
			return nil, fmt.Errorf("%w: answer file %q is outside the work directory", ErrFailed, f)
		}

		data, err := os.ReadFile(filepath.Join(work, f)) //nolint:gosec // a file inside the call's work directory
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrFailed, err)
		}

		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}

			var it T
			if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
				return nil, fmt.Errorf("%w: %s: %w", ErrFailed, f, err)
			}

			items = append(items, it)
		}

		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrFailed, f, err)
		}
	}

	return items, nil
}

type cantTellError struct {
	plugin, call, message string
}

func (e *cantTellError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", e.plugin, e.call, ErrCantTell, e.message)
}

func (e *cantTellError) Unwrap() error { return ErrCantTell }

// CantTellMessage is what a plugin that couldn't tell said about why, on one line, redacted.
func CantTellMessage(err error) string {
	ce, ok := errors.AsType[*cantTellError](err)
	if !ok {
		return ""
	}

	return strings.Join(strings.Fields(strings.ReplaceAll(ce.message, "\n", "; ")), " ")
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}

// logStderr writes a call's stderr to Log under the plugin's name and the call, tokens redacted.
func (p Plugin) logStderr(call, text string) {
	if p.Log == nil || strings.TrimSpace(text) == "" {
		return
	}

	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		if _, err := fmt.Fprintf(p.Log, "%s %s: %s\n", p.Name, call, secret.Redact(line)); err != nil {
			return
		}
	}
}
