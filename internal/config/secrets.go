package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Secrets name the Concourse vars one suite's tasks get, by kind of task:
// each maps an environment variable's name to a var's name, never a value.
type Secrets struct {
	Prepare map[string]string `yaml:"prepare"`
	Batch   map[string]string `yaml:"batch"`
	Whole   map[string]string `yaml:"whole"`
}

func (s Secrets) kinds() map[string]map[string]string {
	return map[string]map[string]string{"prepare": s.Prepare, "batch": s.Batch, "whole": s.Whole}
}

// SecretsFor merges a suite's secrets with the run's for it, kind by
// kind; the run's win per variable.
func (c *Config) SecretsFor(run, suite string) Secrets {
	merge := func(a, b map[string]string) map[string]string {
		if len(a)+len(b) == 0 {
			return nil
		}

		out := map[string]string{}
		maps.Copy(out, a)
		maps.Copy(out, b)

		return out
	}

	s, r := c.Suites[suite].Secrets, c.Runs[run].Suites[suite].Secrets

	return Secrets{Prepare: merge(s.Prepare, r.Prepare), Batch: merge(s.Batch, r.Batch), Whole: merge(s.Whole, r.Whole)}
}

var (
	secretEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	varName   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

const ownPrefix = "PIPELINEGEN_"

// validateParams refuses a var reference in plain params, which would put a
// secret into every task, and pipelinegen's own variables.
func validateParams(where string, params map[string]string, bad func(string, ...any)) {
	for _, k := range slices.Sorted(maps.Keys(params)) {
		if strings.HasPrefix(k, ownPrefix) {
			bad("%s: params may not set %s: the %s variables are pipelinegen's", where, k, ownPrefix)
		}

		if strings.Contains(params[k], "((") {
			bad("%s: params.%s holds a var reference; declare it under secrets for the tasks that need it", where, k)
		}
	}
}

func validateSecrets(where string, secrets, params map[string]string, bad func(string, ...any)) {
	for _, env := range slices.Sorted(maps.Keys(secrets)) {
		switch {
		case !secretEnv.MatchString(env) || strings.HasPrefix(env, ownPrefix):
			bad("%s: %q can't be a secret's variable: a shell name, not starting with %s", where, env, ownPrefix)
		case !varName.MatchString(secrets[env]):
			bad("%s: %s names %q, which is no plain var name", where, env, secrets[env])
		}

		if _, ok := params[env]; ok {
			bad("%s: %s is both a param and a secret", where, env)
		}
	}
}

// CorrelatorsFor are the correlator entries that observe a suite's slices.
func (c *Config) CorrelatorsFor(suite string) []string {
	var out []string

	listed := c.Suites[suite].Correlators

	for _, p := range c.Plugins {
		if p.Kind == KindCorrelator && (listed == nil || slices.Contains(listed, p.Name)) {
			out = append(out, p.Name)
		}
	}

	return out
}

func (c *Config) validateCollectLimits(bad func(string, ...any)) {
	byName := map[string]Plugin{}
	for _, p := range c.Plugins {
		byName[p.Name] = p
	}

	for _, name := range c.SuiteNames() {
		if c.Suites[name].Bind != BindRecorded {
			continue
		}

		var (
			total time.Duration
			each  []string
		)

		for _, n := range c.CorrelatorsFor(name) {
			limit := byName[n].CollectLimit()
			total += limit
			each = append(each, fmt.Sprintf("%s %ds", n, int(limit.Seconds())))
		}

		if total > MaxCollectSeconds*time.Second {
			bad("suite %s: the collect calls of its correlators may take %ds together (%s), more than %ds: "+
				"a mark waits for each of them in turn, and the playwright driver's marks.mjs waits 120s for a mark; lower collect_seconds",
				name, int(total.Seconds()), strings.Join(each, ", "), MaxCollectSeconds)
		}
	}
}

func (c *Config) validateCorrelators(bad func(string, ...any)) {
	observes := map[string]bool{}

	for _, name := range c.SuiteNames() {
		for _, n := range c.Suites[name].Correlators {
			if !slices.ContainsFunc(c.Plugins, func(p Plugin) bool { return p.Name == n && p.Kind == KindCorrelator }) {
				bad("suite %s: correlators names %s, which is no correlator entry of plugins", name, n)
			}
		}

		if c.Suites[name].Bind == BindRecorded {
			for _, n := range c.CorrelatorsFor(name) {
				observes[n] = true
			}
		}
	}

	for _, p := range c.Plugins {
		if p.Kind == KindCorrelator && !observes[p.Name] {
			c.Warnings = append(c.Warnings, "correlator "+p.Name+" observes no recorded suite")
		}
	}
}

func (c *Config) validateSuiteSecrets(bad func(string, ...any)) {
	for _, name := range c.SuiteNames() {
		s := c.Suites[name]
		validateParams("suite "+name, s.Params, bad)

		for kind, m := range s.Secrets.kinds() {
			validateSecrets("suite "+name+" secrets."+kind, m, s.Params, bad)
		}
	}

	for _, run := range c.RunNames() {
		for _, name := range c.Runs[run].RunSuiteNames() {
			where := "run " + run + " suite " + name
			validateParams(where, c.Runs[run].Suites[name].Params, bad)

			params := c.Params(run, name)
			for kind, m := range c.SecretsFor(run, name).kinds() {
				validateSecrets(where+" secrets."+kind, m, params, bad)
			}
		}
	}

	p := c.Concourse.Planner
	validateParams("concourse.planner", p.Params, bad)
	validateSecrets("concourse.planner.secrets", p.Secrets, p.Params, bad)

	if _, ok := p.Secrets[p.GitToken]; p.GitToken != "" && !ok {
		bad("concourse.planner.git_token names %s, which concourse.planner.secrets doesn't declare", p.GitToken)
	}

	// a key going away stays readable, with a warning
	if len(c.Concourse.Vars) > 0 {
		c.Warnings = append(c.Warnings, "concourse.vars is no longer read and will be refused: `pipelinegen pipeline --vars` lists the vars a pipeline needs; remove the key")
	}
}

func (c *Config) writingTokenVars() map[string]bool {
	out := map[string]bool{}

	for _, p := range []string{plugin.PurposeChanges, plugin.PurposeReport, plugin.PurposeStore} {
		if v := c.GitHub.TokenFor(p); v != "" {
			out[v] = true
		}
	}

	return out
}

// WritingSecrets names, for run (every run for ""), each repository task
// that a run taking pull requests gives a writing token as a secret. Such a
// run executes a change's own scripts in those tasks, so a token there must
// only read.
func (c *Config) WritingSecrets(run string) []string {
	writing := c.writingTokenVars()

	var out []string

	for _, r := range c.RunNames() {
		if run != "" && r != run || c.Runs[r].Trigger.PullRequests == nil {
			continue
		}

		for _, suite := range slices.Sorted(maps.Keys(c.Runs[r].Suites)) {
			kinds := c.SecretsFor(r, suite).kinds()

			for _, kind := range slices.Sorted(maps.Keys(kinds)) {
				for _, env := range slices.Sorted(maps.Keys(kinds[kind])) {
					if v := kinds[kind][env]; writing[v] {
						out = append(out, fmt.Sprintf("task %s/%s is given %s, a token that can write, in a run that executes "+
							"a change's own scripts: give it a token that only reads (github.tokens.read)", suite, kind, v))
					}
				}
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// StoreKeySecrets names each repository task given the var of the ssh key
// that writes the store as a secret; nothing of the repository's may hold
// it.
func (c *Config) StoreKeySecrets() []string {
	key := c.Git.SSHKey.Store
	if key == "" {
		return nil
	}

	var out []string

	for _, r := range c.RunNames() {
		for _, suite := range slices.Sorted(maps.Keys(c.Runs[r].Suites)) {
			kinds := c.SecretsFor(r, suite).kinds()

			for _, kind := range slices.Sorted(maps.Keys(kinds)) {
				for _, env := range slices.Sorted(maps.Keys(kinds[kind])) {
					if kinds[kind][env] == key {
						out = append(out, fmt.Sprintf("task %s/%s is given %s, the ssh key that writes the store: "+
							"no task of the repository's may hold it; give it a read-only deploy key of its own", suite, kind, key))
					}
				}
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// validateTaskSecrets refuses a repository task a credential that writes: in
// a run that takes pull requests a writing token, in any run the ssh key
// that writes the store or the var that pushes cached images.
func (c *Config) validateTaskSecrets(bad func(string, ...any)) {
	if c.GitHub.SplitTokens() != nil {
		for _, line := range c.WritingSecrets("") {
			bad("%s", line)
		}
	}

	for _, line := range c.StoreKeySecrets() {
		bad("%s", line)
	}

	for _, line := range c.PushSecrets() {
		bad("%s", line)
	}
}
