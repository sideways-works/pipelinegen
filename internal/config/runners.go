package config

import (
	"encoding/json"
	"slices"
	"strings"
)

// Runners are the runner plugins, in the config's order.
func (c *Config) Runners() []Plugin {
	var out []Plugin

	for _, p := range c.Plugins {
		if p.Kind == KindRunner {
			out = append(out, p)
		}
	}

	return out
}

// RunnerNamed is the runner plugin of that name.
func (c *Config) RunnerNamed(name string) (Plugin, bool) {
	for _, p := range c.Runners() {
		if p.Name == name {
			return p, true
		}
	}

	return Plugin{}, false
}

func (c *Config) runnerNames() []string {
	runners := c.Runners()

	names := make([]string, 0, len(runners))
	for _, p := range runners {
		names = append(names, p.Name)
	}

	return names
}

// RunRunner is the runner that owns the run: its runs.<name>.runner, else the only runner
// configured; empty when it names none and there are several, or none.
func (c *Config) RunRunner(run string) string {
	if r := c.Runs[run].Runner; r != "" {
		return r
	}

	if runners := c.Runners(); len(runners) == 1 {
		return runners[0].Name
	}

	return ""
}

// OwnedRuns are the names of the runs the runner owns, in RunNames' order.
func (c *Config) OwnedRuns(runner string) []string {
	var out []string

	for _, name := range c.RunNames() {
		if c.RunRunner(name) == runner {
			out = append(out, name)
		}
	}

	return out
}

func (c *Config) validateRunRunner(where string, r Run, bad func(string, ...any)) {
	runners := c.runnerNames()

	switch {
	case r.Runner != "":
		if _, ok := c.RunnerNamed(r.Runner); !ok {
			bad("%s: runner %s is no runner plugin of this config (runners: %s)", where, r.Runner, listOrNone(runners))
		}
	case len(runners) > 1:
		bad("%s: %d runners are configured (%s): name the one that owns it in runner", where, len(runners), strings.Join(runners, ", "))
	}
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ", ")
}

// runRunners are the trusted owners of the runs, only with several runners: with one, every run
// is its own and the trusted keys stay as they were before runs named a runner.
func (c *Config) runRunners() map[string]string {
	if len(c.Runners()) < 2 {
		return nil
	}

	return c.owners()
}

// owners are the resolved owner of every run, taken before the trusted keys replace any of it.
func (c *Config) owners() map[string]string {
	out := map[string]string{}
	for _, name := range c.RunNames() {
		out[name] = c.RunRunner(name)
	}

	return out
}

// trustRunRunners gives each run the trusted config also has the owner the trusted config gives
// it, and names the runs whose owner the change moves; a run only the change has keeps its own.
// Trusted keys without run_runners come from a config of one runner at most: the one of own's
// runners that the trusted plugins name.
func (c *Config) trustRunRunners(own map[string]string, ownRunners []string, t Trusted) []string {
	only := ""

	if t.RunRunners == nil {
		for _, name := range ownRunners {
			if slices.ContainsFunc(t.Plugins, func(tp TrustedPlugin) bool { return tp.Name == name }) {
				only = name
			}
		}
	}

	var edited []string

	for _, name := range c.RunNames() {
		if _, ok := t.RunSuites[name]; !ok {
			continue
		}

		trusted, ok := t.RunRunners[name]
		if !ok {
			trusted = only
		}

		if own[name] != trusted {
			edited = append(edited, "runs."+name+".runner")
		}

		r := c.Runs[name]
		r.Runner = t.RunRunners[name]
		c.Runs[name] = r
	}

	return edited
}

// runnerConfigs are, with several runners, each one's config block as runnerConfig makes it.
func (c *Config) runnerConfigs() map[string]json.RawMessage {
	runners := c.Runners()
	if len(runners) < 2 {
		return nil
	}

	out := map[string]json.RawMessage{}

	for _, p := range runners {
		if b := configBlock(p); b != nil {
			out[p.Name] = *b
		}
	}

	return out
}

// trustRunnerConfigs gives each runner the trusted block of its name, with several runners in the
// trusted config, and names each block the change edits.
func (c *Config) trustRunnerConfigs(trusted map[string]json.RawMessage, same func(key string, a, b any)) {
	for i, p := range c.Plugins {
		if p.Kind != KindRunner {
			continue
		}

		raw, ok := trusted[p.Name]
		if !ok {
			raw = json.RawMessage("{}")
		}

		same("plugins."+p.Name+".config", configBlock(p), &raw)

		var block map[string]any
		if json.Unmarshal(raw, &block) != nil || len(block) == 0 {
			block = nil
		}

		c.Plugins[i].Config = block
	}
}
