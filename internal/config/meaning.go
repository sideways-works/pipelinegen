package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The meanings of a changed part of the config: what a change to it can
// change in what a test observes.
const (
	// MeansNothing is a part no test can observe: where results go, how
	// long records are kept, how slices are dealt into batches.
	MeansNothing = "nothing"
	// MeansSelection is a part that decides which tests a change selects,
	// not how they run: a change plans under both configs and unites the
	// plans, so it can't deselect its own tests.
	MeansSelection = "selection"
	// MeansSlices is a part that decides how some slices run (a suite's
	// steps, their settings, its environment): those slices run, never
	// sampled away.
	MeansSlices = "slices"
	// MeansEntry is a plugin's entry, its pin or its config block: what
	// it reaches runs, or its sample while plugin-entry is on.
	MeansEntry = "plugin-entry"
	// MeansFile is a part the planner doesn't understand.
	MeansFile = "file"
)

// Part is one changed part of the config, by its key path, and what it
// means for the tests.
type Part struct {
	Path  string `json:"path"`
	Means string `json:"means"`
}

// Meaning is what a change of the config means for the tests: each changed
// part, and per suite and slice what runs differently. A proposed change's
// head config carries the trusted keys, so a part it can't change in its own
// run is no difference there.
type Meaning struct {
	Parts []Part `json:"parts"`

	base, head *Config
	run        string
	drivers    map[string]bool
}

// MeaningOf compares two configs part by part. run is the run the plan
// is for, empty for a plan for no run, which counts every run's settings.
func MeaningOf(base, head *Config, run string) *Meaning {
	m := &Meaning{base: base, head: head, run: run, drivers: map[string]bool{}}

	bt, ht := tree(base), tree(head)

	var paths [][]string

	diffTrees(nil, bt, ht, &paths)

	for _, p := range paths {
		m.Parts = append(m.Parts, Part{Path: strings.Join(p, "."), Means: m.means(p)})
	}

	slices.SortFunc(m.Parts, func(a, b Part) int { return strings.Compare(a.Path, b.Path) })

	for _, name := range driverNames(base, head) {
		bp, _ := base.Driver(name)
		hp, _ := head.Driver(name)

		if !samePin(bp, hp) {
			m.drivers[name] = true
		}
	}

	return m
}

// Understood tells whether every changed part has a meaning the planner
// acts on: then the config is no changed file. A base read with warnings
// (a key this pipelinegen doesn't know, a value of another type) is not
// understood whole.
func (m *Meaning) Understood() bool {
	if m == nil {
		return false
	}

	if len(m.base.Warnings) > 0 && slices.ContainsFunc(m.base.Warnings, historyWarning) {
		return false
	}

	return !slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Means == MeansFile })
}

func historyWarning(w string) bool {
	return strings.HasPrefix(w, "keys this pipelinegen doesn't read") || strings.HasPrefix(w, "values of another type")
}

// Changed tells whether the configs differ in any part.
func (m *Meaning) Changed() bool { return m != nil && len(m.Parts) > 0 }

// tree is a config as a tree of maps, lists and values, with the plugins
// keyed by name: comments, layout, key order and YAML's alternative forms
// are gone.
func tree(c *Config) any {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil
	}

	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil
	}

	if top, ok := out.(map[string]any); ok {
		if ps, ok := top["plugins"].([]any); ok {
			byName := map[string]any{}

			for _, p := range ps {
				if pm, ok := p.(map[string]any); ok {
					byName[fmt.Sprint(pm["name"])] = pm
				}
			}

			top["plugins"] = byName
		}
	}

	return out
}

func diffTrees(at []string, a, b any, out *[][]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)

	if !aok || !bok {
		if !reflect.DeepEqual(a, b) && (!empty(a) || !empty(b)) {
			*out = append(*out, slices.Clone(at))
		}

		return
	}

	keys := slices.Sorted(maps.Keys(am))
	for k := range bm {
		if _, ok := am[k]; !ok {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	for _, k := range keys {
		diffTrees(append(slices.Clone(at), k), am[k], bm[k], out)
	}
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case string:
		return x == ""
	case bool:
		return !x
	case int:
		return x == 0
	}

	return false
}

var suiteSettings = []struct {
	key string
	get func(m *Meaning, c *Config, suite string) any
}{
	{StepPrepare, func(_ *Meaning, c *Config, s string) any { return c.Suites[s].Prepare }},
	{"run_slice", func(_ *Meaning, c *Config, s string) any { return c.Suites[s].RunSlice }},
	{"image", func(_ *Meaning, c *Config, s string) any { return c.Suites[s].Image }},
	{"privileged", func(_ *Meaning, c *Config, s string) any { return c.Suites[s].Privileged }},
	{"stack", func(_ *Meaning, c *Config, s string) any { return c.Suites[s].Stack }},
	{"params", func(m *Meaning, c *Config, s string) any {
		return m.perRun(c, func(r string) any { return nonEmpty(c.Params(r, s)) })
	}},
	{"secrets", func(m *Meaning, c *Config, s string) any {
		return m.perRun(c, func(r string) any { return c.SecretsFor(r, s) })
	}},
}

var (
	sharedSelection = []string{"follow", "follow_depth", "narrowing", "sample_on"}
	suiteSelection  = append([]string{
		"tests", "tag", "exclude", "helpers", "owner", "extra_deps", "text_inputs", "durations", "group", "bind", "sample",
	}, sharedSelection...)
	// how slices are dealt into batches and share machines: a slice must
	// pass beside any other, in any batch, so none of it is observable.
	suiteNothing = []string{"run_on", "parallel", "batches", "batch_minutes", "empty_recordings"}
	topSelection = append([]string{
		"everything_on", "untested", "macros", "uncovered", "narrowing_alarm", "explain_limit", "rebuild_check",
	}, sharedSelection...)
	topNothing = []string{"pipeline", "github", "git", "store", "keep", "writes", "connectors", "generated", "learn", "limits", "bazel_diff"}
)

func suiteRuns(key string) bool {
	return key == "steps" || key == "whole_suite" || slices.ContainsFunc(suiteSettings, func(s struct {
		key string
		get func(m *Meaning, c *Config, suite string) any
	}) bool {
		return s.key == key
	})
}

// means classifies one changed path; what isn't named here is not
// understood, and the config then counts as a changed file.
func (m *Meaning) means(p []string) string {
	switch p[0] {
	case "suites":
		return suiteMeans(p)
	case "plugins":
		return m.pluginMeans(p)
	case "runs":
		return runMeans(p)
	case "concourse":
		// the root pipeline file declares the images; the bootstrap is
		// the pipelinegen every step runs, slice run included
		if len(p) < 2 || p[1] == "include" || p[1] == "planner" && (len(p) < 3 || p[2] == "bootstrap") {
			return MeansFile
		}

		return MeansNothing
	}

	switch {
	case slices.Contains(topSelection, p[0]):
		return MeansSelection
	case slices.Contains(topNothing, p[0]):
		return MeansNothing
	}

	return MeansFile
}

func suiteMeans(p []string) string {
	if len(p) < 3 {
		// a suite added or removed: its slices run differently from not
		// at all, which SliceRuns tells
		return MeansSlices
	}

	switch key := p[2]; {
	case suiteRuns(key):
		return MeansSlices
	case slices.Contains(suiteSelection, key):
		return MeansSelection
	case slices.Contains(suiteNothing, key):
		return MeansNothing
	case key == "lock" && len(p) > 3 && p[3] != "minutes":
		return MeansNothing
	}

	// a time limit can end a batch that passed before, and the
	// correlators that collect in every batch can fail a slice: no slice
	// in particular, so the config is a changed file
	return MeansFile
}

func (m *Meaning) pluginMeans(p []string) string {
	if len(p) < 2 {
		return MeansFile
	}

	bp, bok := pluginNamed(m.base, p[1])
	hp, hok := pluginNamed(m.head, p[1])

	kind := hp.Kind
	if !hok {
		kind = bp.Kind
	}

	if bok && hok && bp.Kind != hp.Kind {
		return MeansFile
	}

	key := ""
	if len(p) > 2 {
		key = p[2]
	}

	switch kind {
	case KindSBOM, KindCorrelator:
		// its pin unit carries the entry to the slices it binds
		switch key {
		case "", "release", "commit", "config", "kind":
			return MeansEntry
		case "include", "exclude", "fill", "repository", "name":
			return MeansNothing
		case "collect_seconds":
			// a shorter limit can fail the recording of a slice that passed
			// before, in any batch: no slice in particular
			return MeansFile
		}
	case KindConnector:
		// where results go and which changes run: no test can observe it
		return MeansNothing
	case KindDriver:
		switch key {
		case "", "release", "commit", "repository", "name", "kind":
			return MeansEntry
		case "config":
			// the settings of every step that runs it
			return MeansSlices
		case "fill":
			return MeansNothing
		}
	}

	// a runner writes every task, and places it on its workers
	return MeansFile
}

func runMeans(p []string) string {
	if len(p) < 3 {
		// a run added or removed: its suites' settings, which SliceRuns
		// tells
		return MeansSlices
	}

	if p[2] != "suites" {
		return MeansNothing
	}

	if len(p) < 5 {
		return MeansSlices
	}

	switch p[4] {
	case "params", "secrets":
		return MeansSlices
	case "minutes", "lock_minutes":
		return MeansFile
	}

	return MeansFile
}

func pluginNamed(c *Config, name string) (Plugin, bool) {
	for _, p := range c.Plugins {
		if p.Name == name {
			return p, true
		}
	}

	return Plugin{}, false
}

func driverNames(cs ...*Config) []string {
	seen := map[string]bool{}

	for _, c := range cs {
		for _, p := range c.Plugins {
			if p.Kind == KindDriver {
				seen[p.Name] = true
			}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

func samePin(a, b Plugin) bool {
	return a.Repository == b.Repository && a.Commit == b.Commit && reflect.DeepEqual(a.Release, b.Release)
}

// SliceRuns says why a slice of a suite runs differently under the head's
// config than under the base's, by the first part that differs.
func (m *Meaning) SliceRuns(suite string, sl RoutedSlice) (string, bool) {
	if m == nil {
		return "", false
	}

	_, inBase := m.base.Suites[suite]
	_, inHead := m.head.Suites[suite]

	switch {
	case !inHead:
		return "", false
	case !inBase:
		return "suites." + suite + " is new", true
	}

	for _, set := range suiteSettings {
		if !reflect.DeepEqual(set.get(m, m.base, suite), set.get(m, m.head, suite)) {
			return fmt.Sprintf("suites.%s.%s changed", suite, set.key), true
		}
	}

	if why, ok := m.everyStep(suite); ok {
		return why, true
	}

	return m.runStep(suite, sl)
}

func (m *Meaning) perRun(c *Config, of func(run string) any) any {
	runs := []string{m.run}
	if m.run == "" {
		runs = append(runs, c.RunNames()...)
	}

	out := map[string]any{}
	for _, r := range runs {
		out[r] = of(r)
	}

	return out
}

func nonEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}

	return m
}

type stepAt struct {
	Kind     string         `json:"kind"`
	Driver   string         `json:"driver"`
	Image    string         `json:"image"`
	Settings map[string]any `json:"settings"`
	Batch    bool           `json:"batch"`
}

func at(c *Config, st Step) string {
	data, err := json.Marshal(stepAt{Kind: st.Kind(), Driver: st.Driver(), Image: st.Image, Settings: c.DriverConfig(st), Batch: st.Bundles()})
	if err != nil {
		return fmt.Sprint(st)
	}

	return string(data)
}

func (m *Meaning) everyStep(suite string) (string, bool) {
	list := func(c *Config) []string {
		s := c.Suites[suite]

		var out []string

		for _, kind := range []string{StepPrepare, StepSetup} {
			for _, i := range s.StepsOf(kind) {
				out = append(out, at(c, s.Steps[i]))
			}
		}

		return out
	}

	b, h := list(m.base), list(m.head)
	if slices.Equal(b, h) {
		return "", false
	}

	hs := m.head.Suites[suite]

	for _, kind := range []string{StepPrepare, StepSetup} {
		for _, i := range hs.StepsOf(kind) {
			if !slices.Contains(b, at(m.head, hs.Steps[i])) {
				return fmt.Sprintf("suites.%s.steps[%d] (%s %s) changed", suite, i, kind, hs.Steps[i].Driver()), true
			}
		}
	}

	return fmt.Sprintf("suites.%s's prepare and set-up steps changed", suite), true
}

func (m *Meaning) runStep(suite string, sl RoutedSlice) (string, bool) {
	one := func(c *Config) (string, int) {
		s := c.Suites[suite]
		if !s.HasSteps() {
			return "", -1
		}

		i, ok := s.RunStepFor(sl)
		if !ok {
			return "", -1
		}

		return at(c, s.Steps[i]), i
	}

	b, _ := one(m.base)
	h, i := one(m.head)

	if b == h {
		return "", false
	}

	if i < 0 {
		return fmt.Sprintf("suites.%s.steps changed: no run step takes it", suite), true
	}

	st := m.head.Suites[suite].Steps[i]

	return fmt.Sprintf("suites.%s.steps[%d] (run %s), which takes it, changed", suite, i, st.Driver()), true
}

// Drivers are the drivers whose entry's pin changed that run a slice of
// a suite.
func (m *Meaning) Drivers(suite string, sl RoutedSlice) []string {
	if m == nil || len(m.drivers) == 0 {
		return nil
	}

	s, ok := m.head.Suites[suite]
	if !ok {
		return nil
	}

	var out []string

	add := func(name string) {
		if m.drivers[name] && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}

	for _, kind := range []string{StepPrepare, StepSetup} {
		for _, i := range s.StepsOf(kind) {
			add(s.Steps[i].Driver())
		}
	}

	if i, ok := s.RunStepFor(sl); ok {
		add(s.Steps[i].Driver())
	}

	slices.Sort(out)

	return out
}

// WholeChecks are a suite's whole-suite checks whose entry is new or
// changed: each is a test of its own, which runs.
func (m *Meaning) WholeChecks(suite string) []string {
	if m == nil {
		return nil
	}

	var out []string

	for _, w := range m.head.Suites[suite].WholeSuite {
		i := slices.IndexFunc(m.base.Suites[suite].WholeSuite, func(b WholeSuiteCheck) bool { return b.Name == w.Name })
		if i < 0 || !reflect.DeepEqual(m.base.Suites[suite].WholeSuite[i], w) {
			out = append(out, w.Name)
		}
	}

	return out
}
