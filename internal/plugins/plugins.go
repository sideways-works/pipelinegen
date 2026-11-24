// Package plugins runs a repository's sbom and correlator plugins for planning and assembles a
// deps.Tree, keeping every answer in the content cache so a plan asks only about what changed.
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

var ErrPlugins = errors.New("plugins")

type Options struct {
	Log io.Writer

	Config *config.Config
	Dir    string
	// Cache is the core's cache directory: <Cache>/plugins/<name> is each plugin's own,
	// <Cache>/caches/<name> the shared caches plugins declare.
	Cache     string
	Answers   *plugincache.Cache
	Work      string
	Described map[string]plugin.Description
	// Cold asks every plugin about every file and set and compares each answer with what the
	// cache would have served.
	Cold bool
}

type Loaded struct {
	Conf config.Plugin
	P    plugin.Plugin
	Desc plugin.Description

	built     string
	notPinned *NotPinned
}

func (l *Loaded) can(call string) bool { return slices.Contains(l.Desc.Capabilities, call) }

// version and checksum name exactly which build answered, so an answer of one build is never
// served for another.
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

type Set struct {
	o       Options
	Plugins []*Loaded
	offAt   map[string]int
	set     []Override

	mu     sync.Mutex
	report Report
	n      int

	claimed map[string][]plugin.Claim

	pathTables map[string][]string
	answers    map[string]answerIndex

	differences []Difference
}

type Report struct {
	Calls    []CallCount `json:"calls,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
}

type CallCount struct {
	Plugin      string `json:"plugin"`
	Call        string `json:"call"`
	Asked       int    `json:"asked"`
	Cached      int    `json:"cached"`
	Uncacheable int    `json:"uncacheable,omitempty"`
}

func Load(ctx context.Context, o Options) (*Set, error) {
	s := &Set{o: o, offAt: map[string]int{}}

	for _, conf := range o.Config.Plugins {
		if conf.Kind == config.KindRunner || conf.Kind == config.KindDriver || conf.Kind == config.KindForge {
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

	declarer := map[string]string{}

	for _, l := range s.Plugins {
		for _, n := range l.Desc.Narrowings {
			declarer[n.Name] = "plugin " + l.Conf.Name
		}
	}

	for _, n := range CoreNarrowings {
		if _, ok := s.offAt[n.Name]; !ok {
			s.offAt[n.Name] = n.OffAt
			declarer[n.Name] = ByCore
		}
	}

	if err := s.override(o.Config.Narrowings, declarer); err != nil {
		return nil, err
	}

	return s, nil
}

// ByCore is the declarer of the core's own narrowings.
const ByCore = "the core"

// Override is a narrowing whose level the config sets: OffAt in force, Declared the level its
// declarer (By) gives.
type Override struct {
	Name     string `json:"name"`
	OffAt    int    `json:"off_at"`
	Declared int    `json:"declared"`
	By       string `json:"by"`
	Note     string `json:"note"`
}

// override puts the config's levels in place of the declarers': a name nobody declares is
// refused, since it would narrow nothing and the config would only seem to.
func (s *Set) override(levels map[string]config.NarrowingLevel, declarer map[string]string) error {
	for _, name := range sortedKeys(levels) {
		lv := levels[name]

		declared, ok := s.offAt[name]
		if !ok {
			return fmt.Errorf("%w: narrowings.%s: no configured plugin declares the narrowing, and the core has none of that name (%s)",
				ErrPlugins, name, strings.Join(sortedKeys(s.offAt), ", "))
		}

		s.offAt[name] = lv.OffAt
		s.set = append(s.set, Override{Name: name, OffAt: lv.OffAt, Declared: declared, By: declarer[name], Note: lv.Note})
	}

	return nil
}

func (s *Set) Overrides() []Override { return slices.Clone(s.set) }

// NarrowSetupSplit keeps what ran during a batch's setup out of its slices' lists, binding the
// suite's setup target instead.
const NarrowSetupSplit = "setup-split"

// The narrowings the core implements itself, whatever plugins are configured.
const (
	// NarrowSample binds the units every recorded slice of a suite ran to a sample of the suite
	// instead of to every slice.
	NarrowSample = "sample"
	// NarrowSampleBound binds to the sample, too, the units every recorded slice binds only
	// through code every slice ran, and those bound because they run at start-up.
	NarrowSampleBound = "sample-bound"
	// NarrowUnobservable takes a changed file whose units cover it whole and are all as they were
	// for no change to the slices bound by recording, whatever Bazel edge leads there.
	NarrowUnobservable = "unobservable-edits"
	// NarrowPluginEntry binds a plugin's entry in the config to a sample of each suite it reaches
	// instead of to every slice it reaches.
	NarrowPluginEntry = "plugin-entry"
	// NarrowPassedSince compares a slice of a last_passed run from where it last passed in that
	// run, when that is newer than the run's base, and a pull request's slice from the tree its last
	// build tested, carrying over the slices that passed there.
	NarrowPassedSince = "passed-since"
	// NarrowLinkedImports takes an import a package gains or loses for a change to what the imported
	// packages run where they are linked, tree by tree, instead of to the importing package.
	NarrowLinkedImports = "linked-imports"

	// NarrowBuildFiles leaves BUILD and .bzl files out of a recorded slice's data: a change to a
	// rule reaches it through that rule.
	NarrowBuildFiles = "build-files-by-targets"
)

// CoreNarrowings are the core's own narrowings with the level that switches each off, as the
// ladder lists them.
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
	{Name: NarrowLinkedImports, OffAt: 4, Note: "an import a package gains or loses changes, for a slice that links the package, only what " +
		"the imported packages run once linked, and what the slice now reaches through them: their inits, the variables whose " +
		"initialisers may act, the modules they bring; a slice that already linked all of it in both trees runs as it did, " +
		"and code that calls into them changed itself; it rests on the correlator's links naming what runs at start-up"},
	{Name: NarrowPassedSince, OffAt: 4, Note: "a slice that passed at a later commit than the run's base, or in a pull request's last build, " +
		"was tested on everything up to it; it rests on that one pass, so a flaky pass moves its base too; the nightly runs it"},
	{Name: NarrowBuildFiles, OffAt: 5, Note: "a BUILD or .bzl file a recorded slice's filegroups hold reaches it through the rules it declares, " +
		"which bazel-diff hashes, and through the members it lists; it rests on the tests that read such a file's text being declared (text_inputs)"},
}

func CoreOffAt(name string) int {
	for _, n := range CoreNarrowings {
		if n.Name == name {
			return n.OffAt
		}
	}

	return 0
}

// Descriptions are the plugins' descriptions by name, for a later step's Options.Described.
func (s *Set) Descriptions() map[string]plugin.Description {
	out := make(map[string]plugin.Description, len(s.Plugins))
	for _, l := range s.Plugins {
		out[l.Conf.Name] = l.Desc
	}

	return out
}

// Flush writes the answers made since the last Flush to the repository's cache refs, when the
// cache pushes.
func (s *Set) Flush(ctx context.Context) error {
	if s.o.Answers == nil {
		return nil
	}

	return s.o.Answers.Flush(ctx)
}

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

func (s *Set) NotPinned() []NotPinned {
	var out []NotPinned

	for _, l := range s.Plugins {
		if l.notPinned != nil {
			out = append(out, *l.notPinned)
		}
	}

	return out
}

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
