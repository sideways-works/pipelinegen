package plugin

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
	pk "github.com/sideways-works/plugkit/plugin"
)

// ErrCantTell is a plugin's answer that it can't tell, for this tree or this slice.
var ErrCantTell = pk.ErrCantTell

var ErrFailed = pk.ErrFailed

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
	// a plugin reads files: it gets no token and no network settings
	run := pk.Plugin{Name: p.Name, Path: p.Path, Dir: p.Dir, Env: secret.PluginEnv(p.cacheEnv()), Timeout: p.Timeout, Log: p.Log}

	return run.Call(ctx, call, req, answer)
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

	if err := CheckKind(d.Kind); err != nil {
		return d, fmt.Errorf("%w: %s says it is: %w", ErrFailed, p.Name, err)
	}

	return d, nil
}

// ReadLines appends to items the JSON Lines of T in an answer's files in the work directory.
func ReadLines[T any](work string, files []string, items []T) ([]T, error) {
	return pk.ReadLines(work, files, items)
}

// CantTellMessage is what a plugin that couldn't tell said about why, on one line, redacted.
func CantTellMessage(err error) string { return pk.CantTellMessage(err) }
