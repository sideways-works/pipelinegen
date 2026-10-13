// Package plugins runs a repository's sbom and correlator plugins for
// planning and assembles what they say into a deps.Tree, keeping every
// answer in the content cache so a plan asks only about what changed.
package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrPlugins wraps what goes wrong with the plugins as a whole.
var ErrPlugins = errors.New("plugins")

// Options say where the plugins are and what they keep.
type Options struct {
	Log io.Writer

	Config *config.Config
	Dir    string
	// Cache is the core's cache directory: <Cache>/plugins/<name> is each
	// plugin's own, <Cache>/caches/<name> the shared caches plugins declare.
	Cache     string
	Answers   *plugincache.Cache
	Work      string
	Described map[string]plugin.Description
	// Cold asks every plugin about every file and set and compares each
	// answer with what the cache would have served.
	Cold bool
}

// Loaded is one configured plugin, described.
type Loaded struct {
	Conf config.Plugin
	P    plugin.Plugin
	Desc plugin.Description

	built     string
	notPinned *NotPinned
}

func (l *Loaded) can(call string) bool { return slices.Contains(l.Desc.Capabilities, call) }

// version and checksum name exactly which build answered, so an answer of
// one build is never served for another.
func (l *Loaded) version() (string, string) {
	v, sum := "commit", l.Conf.Commit
	if r := l.Conf.Release; r != nil {
		v, sum = r.Version, r.Pin()
	}

	if l.built != "" {
		sum = l.built
	}

	return v, sum
}

// Set is the configured plugins of a repository, described.
type Set struct {
	o       Options
	Plugins []*Loaded
	offAt   map[string]int

	mu     sync.Mutex
	report Report
	n      int

	claimed map[string][]plugin.Claim

	pathTables map[string][]string
	answers    map[string]answerIndex

	differences []Difference
}

// Report is what a plan records about its plugins.
type Report struct {
	Calls    []CallCount `json:"calls,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
}

// CallCount is one plugin call's share of a plan.
type CallCount struct {
	Plugin      string `json:"plugin"`
	Call        string `json:"call"`
	Asked       int    `json:"asked"`
	Cached      int    `json:"cached"`
	Uncacheable int    `json:"uncacheable,omitempty"`
}

// Load describes every sbom and correlator plugin of the config.
func Load(ctx context.Context, o Options) (*Set, error) {
	s := &Set{o: o, offAt: map[string]int{}}

	for _, conf := range o.Config.Plugins {
		if conf.Kind == config.KindRunner || conf.Kind == config.KindDriver {
			continue
		}

		l := &Loaded{Conf: conf, P: plugin.Plugin{
			Name: conf.Name, Path: filepath.Join(o.Dir, conf.Name), Config: conf.Config,
			Cache: filepath.Join(o.Cache, "plugins", conf.Name), CachesDir: filepath.Join(o.Cache, "caches"),
			Log: o.Log,
		}}

		l.built, l.notPinned = build(conf, o.Dir, l.P.Path)

		if d, ok := o.Described[conf.Name]; ok {
			l.Desc = d
		} else if err := l.P.Call(ctx, plugin.CallDescribe, l.P.NewRequest(s.work()), &l.Desc); err != nil {
			return nil, err
		}

		if l.Desc.Kind != conf.Kind {
			return nil, fmt.Errorf("%w: %s says it is a %s, the config a %s", ErrPlugins, conf.Name, l.Desc.Kind, conf.Kind)
		}

		l.P.Caches = l.Desc.Caches

		if missing := MissingNeeds(l.Desc.Needs, secret.PluginEnv(nil)); len(missing) > 0 {
			s.warnf("plugin %s needs %s, which this machine's PATH lacks: expect its calls to fail or to answer can't tell; "+
				"in a pipeline the planner's image or concourse.planner.prepare must provide it", conf.Name, strings.Join(missing, ", "))
		}

		for _, n := range l.Desc.Narrowings {
			s.offAt[n.Name] = n.OffAt
		}

		s.Plugins = append(s.Plugins, l)
	}

	for _, n := range CoreNarrowings {
		if _, ok := s.offAt[n.Name]; !ok {
			s.offAt[n.Name] = n.OffAt
		}
	}

	return s, nil
}

// NarrowSetupSplit keeps what ran during a batch's setup out of its
// slices' lists, binding the suite's setup target instead.
const NarrowSetupSplit = "setup-split"

// The narrowings the core implements itself, whatever plugins are
// configured.
const (
	// NarrowSample binds the units every recorded slice of a suite ran to
	// a sample of the suite instead of to every slice.
	NarrowSample = "sample"
	// NarrowSampleBound binds to the sample, too, the units every recorded
	// slice binds only through code every slice ran, and those bound
	// because they run at start-up.
	NarrowSampleBound = "sample-bound"
	// NarrowUnobservable takes a changed file whose sbom says its units
	// cover it whole, and whose units are all as they were, for no change
	// to the slices bound by recording, whatever Bazel edge leads there.
	NarrowUnobservable = "unobservable-edits"
	// NarrowPluginEntry binds a plugin's entry in the config to a sample of
	// each suite the entry reaches instead of to every slice it reaches.
	NarrowPluginEntry = "plugin-entry"
)

// CoreNarrowings are the core's own narrowings with the level that
// switches each off, as the ladder lists them.
var CoreNarrowings = []plugin.Narrowing{
	{Name: NarrowSample, OffAt: 3, Note: "code every slice runs the same way breaks them alike, " +
		"but a sample can still miss the one slice it breaks differently; the nightly runs the rest"},
	{Name: NarrowSampleBound, OffAt: 3, Note: "what every slice reaches only through code every slice ran, " +
		"or runs at start-up, is exercised as that code is; the sample's own argument, and the nightly behind it"},
	{Name: NarrowPluginEntry, OffAt: 4, Note: "a correlator's entry changes what is recorded and bound, not what runs, and an sbom's entry " +
		"how units are listed and digested, not the code; a sample checks the new entry on each suite it reaches, a unit that really " +
		"changed still selects on its own, and the nightly runs and records the rest; off, what the entry reaches runs, beside any sample_on sample"},
	{Name: NarrowUnobservable, OffAt: 4, Note: "a file whose sbom covers it whole and whose units are as they were " +
		"runs as it did; it rests on the sbom's parse being complete and on the tests that read the file's text being declared (text_inputs)"},
}

// Descriptions are the plugins' descriptions by name, for a later step's
// Options.Described.
func (s *Set) Descriptions() map[string]plugin.Description {
	out := make(map[string]plugin.Description, len(s.Plugins))
	for _, l := range s.Plugins {
		out[l.Conf.Name] = l.Desc
	}

	return out
}

// Flush writes the answers made since the last Flush to the repository's
// cache refs, when the cache pushes.
func (s *Set) Flush(ctx context.Context) error {
	if s.o.Answers == nil {
		return nil
	}

	return s.o.Answers.Flush(ctx)
}

// Report returns what the plans so far recorded.
func (s *Set) Report() Report {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := s.report
	sort.Slice(r.Calls, func(i, j int) bool {
		if r.Calls[i].Plugin != r.Calls[j].Plugin {
			return r.Calls[i].Plugin < r.Calls[j].Plugin
		}

		return r.Calls[i].Call < r.Calls[j].Call
	})

	return r
}

func (s *Set) count(name, call string, asked, cached int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, c := range s.report.Calls {
		if c.Plugin == name && c.Call == call {
			s.report.Calls[i].Asked += asked
			s.report.Calls[i].Cached += cached

			return
		}
	}

	s.report.Calls = append(s.report.Calls, CallCount{Plugin: name, Call: call, Asked: asked, Cached: cached})
}

func (s *Set) notCacheable(name, call string) {
	s.count(name, call, 0, 0)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, c := range s.report.Calls {
		if c.Plugin == name && c.Call == call {
			s.report.Calls[i].Uncacheable++
		}
	}
}

// NotPinned are the plugins whose binary is not the file their pin names.
func (s *Set) NotPinned() []NotPinned {
	var out []NotPinned

	for _, l := range s.Plugins {
		if l.notPinned != nil {
			out = append(out, *l.notPinned)
		}
	}

	return out
}

// Warnf adds a plan-level warning, once.
func (s *Set) Warnf(format string, args ...any) { s.warnf(format, args...) }

func (s *Set) warnf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w := fmt.Sprintf(format, args...)
	if !slices.Contains(s.report.Warnings, w) {
		s.report.Warnings = append(s.report.Warnings, w)
	}
}

func (s *Set) work() string {
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()

	dir := filepath.Join(s.o.Work, fmt.Sprintf("call-%d", n))
	_ = os.MkdirAll(dir, 0o750) //nolint:errcheck // a failure shows as the call's own

	return dir
}

func (l *Loaded) key(call string, content ...string) plugincache.Key {
	v, sum := l.version()

	return plugincache.Key{Plugin: l.Conf.Name, Version: v, SHA256: sum, Config: l.Conf.Config, Call: call, Content: content}
}

func matchAny(patterns []string, p string) bool {
	return slices.ContainsFunc(patterns, func(g string) bool { return config.MatchGlob(g, p) })
}

func isToolchain(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "toolchain")
}
