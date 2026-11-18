package config

import (
	"fmt"
	"maps"
	"slices"
)

// Limits are the time limits, in minutes, of a pipeline's steps besides a suite's own tasks, so
// a step that hangs ends and says which limit it reached.
type Limits struct {
	// Plan bounds each task of a run's planning (the prepare command and the plan) and of its
	// sweep.
	Plan  int `yaml:"plan"`
	Steps int `yaml:"steps"`
}

// The default time limits, in minutes.
const (
	// DefaultSuiteMinutes bounds each of a suite's tasks: its prepare, each batch and each
	// whole-suite check.
	DefaultSuiteMinutes = 120
	DefaultPlanMinutes  = 120
	DefaultStepMinutes  = 20
	DefaultWaitMinutes  = 480
)

func (l Limits) PlanMinutes() int {
	if l.Plan > 0 {
		return l.Plan
	}

	return DefaultPlanMinutes
}

func (l Limits) StepMinutes() int {
	if l.Steps > 0 {
		return l.Steps
	}

	return DefaultStepMinutes
}

// WaitLength is how many minutes a continuous run's build waits for the round in progress.
func (r Run) WaitLength() int {
	if r.WaitMinutes > 0 {
		return r.WaitMinutes
	}

	return DefaultWaitMinutes
}

// SuiteMinutes bounds each of a suite's tasks in a run: the run's minutes for it, else the
// suite's (or lock.minutes, its old spelling), else the default.
func (c *Config) SuiteMinutes(run, suite string) int {
	s := c.Suites[suite]
	rs := c.Runs[run].Suites[suite]

	switch {
	case rs.Minutes > 0:
		return rs.Minutes
	case rs.LockMinutes > 0:
		return rs.LockMinutes
	case s.Minutes > 0:
		return s.Minutes
	case s.Lock != nil && s.Lock.Minutes > 0:
		return s.Lock.Minutes
	}

	return DefaultSuiteMinutes
}

// SuiteMinutesKey is the key a suite's limit in a run is raised by: the run's own when it sets
// one, else the suite's.
func (c *Config) SuiteMinutesKey(run, suite string) string {
	if rs := c.Runs[run].Suites[suite]; rs.Minutes > 0 || rs.LockMinutes > 0 {
		return fmt.Sprintf("runs.%s.suites.%s.minutes", run, suite)
	}

	return fmt.Sprintf("suites.%s.minutes", suite)
}

func (c *Config) validateLimits(bad func(string, ...any)) {
	if c.Limits.Plan < 0 || c.Limits.Steps < 0 {
		bad("limits: plan and steps are minutes, at least 1")
	}

	for _, name := range c.SuiteNames() {
		s := c.Suites[name]

		if s.Minutes < 0 {
			bad("suite %s: minutes must be at least 1", name)
		}

		if l := s.Lock; l != nil && l.Minutes > 0 && s.Minutes > 0 && l.Minutes != s.Minutes {
			bad("suite %s: minutes is %d and lock.minutes %d: lock.minutes is the old spelling of minutes; keep minutes alone", name, s.Minutes, l.Minutes)
		}

		if s.BatchMinutes > 0 && c.SuiteMinutes("", name) < s.BatchMinutes {
			bad("suite %s: its time limit (%d minutes) is shorter than batch_minutes (%d), the time each batch is dealt to take", name, c.SuiteMinutes("", name), s.BatchMinutes)
		}
	}

	for _, run := range c.RunNames() {
		r := c.Runs[run]

		if r.WaitMinutes != 0 && (!r.Trigger.Continuous || r.WaitMinutes < 1) {
			bad("run %s: wait_minutes is for a continuous run, and at least 1", run)
		}

		for _, suite := range slices.Sorted(maps.Keys(r.Suites)) {
			rs := r.Suites[suite]

			if rs.Minutes < 0 || rs.LockMinutes < 0 {
				bad("run %s: suite %s: minutes must be at least 1", run, suite)
			}

			if rs.Minutes > 0 && rs.LockMinutes > 0 && rs.Minutes != rs.LockMinutes {
				bad("run %s: suite %s: minutes is %d and lock_minutes %d: lock_minutes is the old spelling of minutes; keep minutes alone", run, suite, rs.Minutes, rs.LockMinutes)
			}

			if b := c.Suites[suite].BatchMinutes; b > 0 && c.SuiteMinutes(run, suite) < b {
				bad("run %s: suite %s: its time limit (%d minutes) is shorter than batch_minutes (%d)", run, suite, c.SuiteMinutes(run, suite), b)
			}
		}
	}
}

func (c *Config) oldMinutes() {
	for _, name := range c.SuiteNames() {
		ro := c.Suites[name].RunOn
		if ro.SerialGroup != "" {
			c.Notices = append(c.Notices, fmt.Sprintf("suites.%s.run_on.serial_group is no longer read: a suite's builds run side by side; remove it", name))
		}

		if ro.MaxInFlight != 0 {
			c.Notices = append(c.Notices, fmt.Sprintf("suites.%s.run_on.max_in_flight is no longer read: a suite's builds run side by side; remove it", name))
		}
	}

	if len(c.Concourse.SerialGroups) > 0 {
		c.Notices = append(c.Notices, "concourse.serial_groups is no longer read: no job is in a serial group; remove it")
	}

	for _, run := range c.RunNames() {
		if c.Runs[run].TurnMinutes != 0 {
			c.Notices = append(c.Notices, fmt.Sprintf("runs.%s.turn_minutes is no longer read: a round of the run counts as dead after runs.%s.idle_minutes (%d by default here); remove turn_minutes", run, run, c.IdleLength(run)))
		}
	}

	for _, name := range c.SuiteNames() {
		if c.Suites[name].Lock != nil {
			c.Notices = append(c.Notices, fmt.Sprintf("suites.%s.lock is no longer read: a suite's batches start when their node has room; remove it", name))
		}

		if l := c.Suites[name].Lock; l != nil && l.Minutes > 0 {
			c.OldSpellings = append(c.OldSpellings, fmt.Sprintf("suites.%s.lock.minutes is the old spelling of suites.%s.minutes, which needs no lock: write minutes", name, name))
		}
	}

	for _, run := range c.RunNames() {
		for _, suite := range slices.Sorted(maps.Keys(c.Runs[run].Suites)) {
			if c.Runs[run].Suites[suite].LockMinutes > 0 {
				c.OldSpellings = append(c.OldSpellings, fmt.Sprintf("runs.%s.suites.%s.lock_minutes is the old spelling of runs.%s.suites.%s.minutes: write minutes", run, suite, run, suite))
			}
		}
	}
}
