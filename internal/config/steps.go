package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// KindDriver is a plugin that runs a suite's slices.
const KindDriver = "driver"

// BuiltinCommand is the driver built into the tool.
const BuiltinCommand = "command"

// The kinds of step a suite's steps list holds, in the order they run.
const (
	StepPrepare = "prepare"
	StepSetup   = "setup"
	StepRun     = "run"
)

// Step is one entry of a suite's steps: a driver that prepares once per
// run, sets up once per batch, or runs the slices routed to it.
type Step struct {
	// Prepare, Setup and Run name the step's driver (a driver plugin's
	// entry, or BuiltinCommand); a step names exactly one of them.
	Prepare string `json:"prepare,omitempty" yaml:"prepare,omitempty"`
	Setup   string `json:"setup,omitempty"   yaml:"setup,omitempty"`
	Run     string `json:"run,omitempty"     yaml:"run,omitempty"`
	// Take routes slices to a run step: path patterns for test files,
	// "kind:<rule kind>" and label patterns ("//services/...",
	// "//pkg:target") for targets. Left out on a suite's only run step,
	// every slice.
	Take []string `json:"take,omitempty" yaml:"take,omitempty"`
	// Batch lets a run step's driver run the slices it is given together,
	// as its tool allows (a package's files, a batch's targets); off, every
	// slice is an invocation of its own.
	Batch *bool `json:"batch,omitempty" yaml:"batch,omitempty"`

	Image string `json:"image,omitempty" yaml:"image,omitempty"`

	With map[string]any `json:"with,omitempty" yaml:"with,omitempty"`
}

// Kind is the step's kind: StepPrepare, StepSetup or StepRun.
func (s Step) Kind() string {
	switch {
	case s.Prepare != "":
		return StepPrepare
	case s.Setup != "":
		return StepSetup
	default:
		return StepRun
	}
}

// Driver is the name of the step's driver.
func (s Step) Driver() string { return s.Prepare + s.Setup + s.Run }

// Bundles tells whether the step's driver may run its slices together.
func (s Step) Bundles() bool { return s.Batch != nil && *s.Batch }

// HasSteps tells whether the suite is run by drivers rather than task
// files.
func (s Suite) HasSteps() bool { return len(s.Steps) > 0 }

// StepsOf are the suite's steps of one kind, by their index in its list.
func (s Suite) StepsOf(kind string) []int {
	var out []int

	for i, st := range s.Steps {
		if st.Kind() == kind {
			out = append(out, i)
		}
	}

	return out
}

// RoutedSlice is what routing reads of a slice: a test file's path, or a
// target's label and rule kind.
type RoutedSlice struct {
	Name  string
	Label string
	Kind  string
}

// Takes tells whether a run step takes the slice. A step without take
// takes every slice: validation lets only a suite's one run step leave it
// out.
func (s Step) Takes(sl RoutedSlice) bool {
	if len(s.Take) == 0 {
		return true
	}

	return slices.ContainsFunc(s.Take, func(p string) bool { return takeMatches(p, sl) })
}

func takeMatches(p string, sl RoutedSlice) bool {
	switch {
	case strings.HasPrefix(p, "kind:"):
		return sl.Kind != "" && sl.Kind == strings.TrimPrefix(p, "kind:")
	case isLabelPattern(p):
		return sl.Label != "" && labelMatches(p, sl.Label)
	default:
		return MatchGlob(p, sl.Name)
	}
}

func isLabelPattern(p string) bool { return strings.HasPrefix(p, "//") || strings.HasPrefix(p, "@") }

func labelMatches(p, label string) bool {
	if pkg, ok := strings.CutSuffix(p, "/..."); ok {
		lpkg, _, _ := strings.Cut(label, ":")

		return lpkg == pkg || strings.HasPrefix(lpkg, pkg+"/") || pkg == "/" || pkg == "//"
	}

	if pkg, ok := strings.CutSuffix(p, ":all"); ok {
		lpkg, _, _ := strings.Cut(label, ":")

		return lpkg == pkg
	}

	return p == label
}

// RunStepFor is the index of the suite's run step that takes the slice;
// false when none or several do, which the check refuses.
func (s Suite) RunStepFor(sl RoutedSlice) (int, bool) {
	found := -1

	for _, i := range s.StepsOf(StepRun) {
		if !s.Steps[i].Takes(sl) {
			continue
		}

		if found >= 0 {
			return -1, false
		}

		found = i
	}

	return found, found >= 0
}

// DriverConfig is a step's settings: its driver entry's config with the
// step's with over it, key by key.
func (c *Config) DriverConfig(st Step) map[string]any {
	out := map[string]any{}

	for _, p := range c.Plugins {
		if p.Kind == KindDriver && p.Name == st.Driver() {
			maps.Copy(out, p.Config)
		}
	}

	maps.Copy(out, st.With)

	return out
}

// Driver is the driver entry a step names; false for the built-in one or
// a name no entry has.
func (c *Config) Driver(name string) (Plugin, bool) {
	for _, p := range c.Plugins {
		if p.Kind == KindDriver && p.Name == name {
			return p, true
		}
	}

	return Plugin{}, false
}

func (c *Config) validateSteps(name string, s Suite, bad func(string, ...any)) {
	if !s.HasSteps() {
		return
	}

	if s.RunSlice != "" || s.Prepare != "" {
		bad("suite %s: has steps and task files (run_slice, prepare); a suite is run by one or the other", name)
	}

	order := map[string]int{StepPrepare: 0, StepSetup: 1, StepRun: 2}
	last := 0
	runs := s.StepsOf(StepRun)

	for i, st := range s.Steps {
		where := fmt.Sprintf("suite %s: steps[%d]", name, i)

		named := 0

		for _, d := range []string{st.Prepare, st.Setup, st.Run} {
			if d != "" {
				named++
			}
		}

		if named != 1 {
			bad("%s: names one driver as prepare, setup or run", where)

			continue
		}

		if _, ok := c.Driver(st.Driver()); !ok && st.Driver() != BuiltinCommand {
			bad("%s: no driver %q: name a plugin entry of kind driver, or %s", where, st.Driver(), BuiltinCommand)
		}

		if order[st.Kind()] < last {
			bad("%s: a %s step after a %s step; steps run prepare, then setup, then run", where, st.Kind(), kindAt(last))
		}

		last = max(last, order[st.Kind()])

		if st.Kind() != StepRun && (st.Batch != nil || len(st.Take) > 0) {
			bad("%s: batch and take belong to run steps", where)
		}

		if st.Image != "" && st.Kind() != StepPrepare {
			bad("%s: only a prepare step names an image; set-up and run steps share the batch task's", where)
		}

		if st.Kind() == StepRun && len(st.Take) == 0 && len(runs) > 1 {
			bad("%s: the suite has %d run steps, so each says what it takes", where, len(runs))
		}

		for _, p := range st.Take {
			c.validateTake(where, p, bad)
		}
	}

	if len(runs) == 0 {
		bad("suite %s: its steps have no run step", name)
	}
}

func kindAt(n int) string {
	return []string{StepPrepare, StepSetup, StepRun}[n]
}

func (c *Config) validateTake(where, p string, bad func(string, ...any)) {
	switch {
	case strings.HasPrefix(p, "kind:"):
		if strings.TrimPrefix(p, "kind:") == "" {
			bad("%s: take %q names no rule kind", where, p)
		}
	case isLabelPattern(p):
		if !strings.Contains(p, "//") {
			bad("%s: take %q is no label pattern", where, p)
		}
	default:
		if err := checkGlob(p); err != nil {
			bad("%s: take %q: %v", where, p, err)
		}
	}
}

func (c *Config) suiteSteps() map[string][]Step {
	var out map[string][]Step

	for _, name := range c.SuiteNames() {
		if s := c.Suites[name]; s.HasSteps() {
			if out == nil {
				out = map[string][]Step{}
			}

			out[name] = s.Steps
		}
	}

	return out
}

// trustSteps gives every suite the trusted config's steps, or none: a
// proposed change runs its suites as the trusted config says.
func (c *Config) trustSteps(trusted map[string][]Step) {
	for name, s := range c.Suites {
		if !s.HasSteps() && len(trusted[name]) == 0 {
			continue
		}

		s.Steps = trusted[name]
		c.Suites[name] = s
	}
}
