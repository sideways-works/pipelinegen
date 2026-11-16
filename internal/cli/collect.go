package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Each correlator is loaded on its own: a failing collector must not keep the others from their call,
// or one that keeps an offset in a log would give this slice's part to the next one.
type batchPlugins struct {
	sets []correlatorSet
	work string
}

type correlatorSet struct {
	name string
	set  *plugins.Set
	err  error
}

// describedFile keeps what the correlators said of themselves, so later steps of the batch start none
// of them only to ask.
const describedFile = "described.json"

// A correlator whose describe fails is kept with its error, which its first collect returns, so the
// slice errors and names it.
func loadCorrelators(ctx context.Context, cfg *config.Config, suite, tools, kept string) (*batchPlugins, error) {
	work, err := os.MkdirTemp("", "pipelinegen-plugins-")
	if err != nil {
		return nil, fmt.Errorf("plugins: %w", err)
	}

	b := &batchPlugins{work: work}
	described := readDescribed(kept)
	learned := false

	// Only the correlators the suite names observe its slices, so another one's "can't tell" can't mark them.
	observing := cfg.CorrelatorsFor(suite)

	for i, p := range cfg.Plugins {
		if p.Kind != config.KindCorrelator || !slices.Contains(observing, p.Name) {
			continue
		}

		one := *cfg
		one.Plugins = []config.Plugin{p}

		set, err := plugins.Load(ctx, plugins.Options{
			Config: &one, Dir: filepath.Join(tools, "plugins"), Described: described, Log: os.Stderr,
			Cache: filepath.Join(work, "cache"), Work: filepath.Join(work, fmt.Sprintf("work-%d", i)),
			Answers: &plugincache.Cache{Dir: filepath.Join(work, "answers")},
		})
		b.sets = append(b.sets, correlatorSet{name: p.Name, set: set, err: err})

		if _, ok := described[p.Name]; !ok && err == nil {
			maps.Copy(described, set.Descriptions())

			learned = true
		}
	}

	if learned && kept != "" {
		writeDescribed(kept, described)
	}

	return b, nil
}

const claimedFile = "claimed.json"

// A batch's tree doesn't change between its steps, so the setup's end works the claims out once and the
// slices' steps read them; claims that can't be worked out are left out.
func (b *batchPlugins) claim(ctx context.Context, root, path string) {
	if b == nil || root == "" {
		return
	}

	kept := map[string][]plugin.Claim{}

	if path != "" {
		if data, err := os.ReadFile(path); err == nil { //nolint:gosec // the batch's own result directory
			if json.Unmarshal(data, &kept) != nil {
				kept = map[string][]plugin.Claim{}
			}
		}
	}

	learned := false

	for _, c := range b.sets {
		if c.err != nil {
			continue
		}

		if _, ok := kept[c.name]; !ok {
			got, err := c.set.Claims(ctx, root)
			if err != nil {
				continue
			}

			maps.Copy(kept, got)

			learned = true
		}

		c.set.SetClaimed(map[string][]plugin.Claim{c.name: kept[c.name]})
	}

	if learned && path != "" {
		if data, err := json.Marshal(kept); err == nil {
			writeKept(path, data)
		}
	}
}

func readDescribed(path string) map[string]plugin.Description {
	out := map[string]plugin.Description{}

	if path == "" {
		return out
	}

	data, err := os.ReadFile(path) //nolint:gosec // the batch's own result directory
	if err != nil || json.Unmarshal(data, &out) != nil {
		return map[string]plugin.Description{}
	}

	return out
}

func writeDescribed(path string, d map[string]plugin.Description) {
	data, err := json.Marshal(d)
	if err != nil {
		return
	}

	writeKept(path, data)
}

// writeKept writes whole or not at all: a failed write only costs the later steps asking again.
func writeKept(path string, data []byte) {
	if os.MkdirAll(filepath.Dir(path), 0o750) != nil {
		return
	}

	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}

	if os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp) //nolint:errcheck // a leftover the next write replaces
	}
}

func (b *batchPlugins) close() error {
	if b == nil {
		return nil
	}

	if err := os.RemoveAll(b.work); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}

	return nil
}

// collecting includes the correlators whose describe failed, as they can't be ruled out.
func (b *batchPlugins) collecting() []string {
	if b == nil {
		return nil
	}

	var out []string

	for _, c := range b.sets {
		if c.err != nil || slices.ContainsFunc(c.set.Plugins, func(l *plugins.Loaded) bool {
			return slices.Contains(l.Desc.Capabilities, plugin.CallCollect)
		}) {
			out = append(out, c.name)
		}
	}

	return out
}

func (b *batchPlugins) collectors(cfg *config.Config, suite string, deadline time.Time) []report.Collector {
	if b == nil {
		return nil
	}

	out := make([]report.Collector, 0, len(b.sets))

	for _, c := range b.sets {
		out = append(out, setCollector{c: c, stack: cfg.Suites[suite].Stack, deadline: deadline})
	}

	return out
}

// collectMargin stops a collect call this long before the batch's limit, so the batch still ends and
// writes its journal.
const collectMargin = 30 * time.Second

type setCollector struct {
	c     correlatorSet
	stack bool
	// deadline is zero when unknown, as in a task file's own steps.
	deadline time.Time
}

// Collect asks the correlator to gather what ran into its own directory under dir.
func (s setCollector) Collect(ctx context.Context, action, slice, dir string) error {
	if s.c.err != nil {
		return fmt.Errorf("collect: %w", s.c.err)
	}

	if !s.deadline.IsZero() {
		var cancel context.CancelFunc

		ctx, cancel = context.WithDeadline(ctx, s.deadline.Add(-collectMargin))
		defer cancel()
	}

	if err := s.c.set.Collect(ctx, action, slice, dir, s.stack); err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	return nil
}

// Observe has every observing correlator say what a slice touched; one that couldn't be loaded, or whose
// observe failed, errors the slice.
func (b *batchPlugins) Observe(ctx context.Context, source, slice, setup string, parts []string) ([]report.Observation, error) {
	var out []report.Observation

	for _, c := range b.sets {
		if c.err != nil {
			return nil, fmt.Errorf("observe: %w", c.err)
		}

		obs, err := c.set.Observe(ctx, source, slice, setup, parts)
		if err != nil {
			return nil, fmt.Errorf("observe: %w", err)
		}

		for _, o := range obs {
			out = append(out, report.Observation{Plugin: o.Plugin, CantTell: o.CantTell, Reason: o.Reason, Touched: o.Touched, SetupLike: o.SetupLike})
		}
	}

	return out, nil
}

var errNoCollector = errors.New("no collector")

// needCollector refuses a recording run of a suite with a stack when no correlator collects: its slices
// would record nothing of the stack.
func needCollector(cfg *config.Config, b *batchPlugins, suite string) error {
	if !cfg.Suites[suite].Stack || len(b.collecting()) > 0 {
		return nil
	}

	return fmt.Errorf("%w: suite %s runs a stack (stack: true), but no configured correlator collects its coverage: add one whose describe lists collect (for Go servers, the Go correlator) under plugins", errNoCollector, suite)
}
