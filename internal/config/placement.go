package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// PlacementKey is the key of a runner's own config block that says where the
// pipeline's steps run; the core reads nothing inside it, only whether it is
// there.
const PlacementKey = "placement"

func retiredPlacement(data []byte) []string {
	var doc struct {
		Concourse struct {
			Planner      map[string]any `yaml:"planner"`
			ResourceTags yaml.Node      `yaml:"resource_tags"`
			Bookkeeping  yaml.Node      `yaml:"bookkeeping"`
		} `yaml:"concourse"`
		Suites  map[string]map[string]any `yaml:"suites"`
		Plugins []namedPlugin             `yaml:"plugins"`
	}

	// a config this can't read is refused by the decoding that follows,
	// with its own message; a value of another type only leaves its key
	// out here
	var te *yaml.TypeError
	if err := yaml.Unmarshal(data, &doc); err != nil && !errors.As(err, &te) {
		return nil
	}

	runner := "concourse"
	if i := slices.IndexFunc(doc.Plugins, func(p namedPlugin) bool { return p.Kind == KindRunner && p.Name != "" }); i >= 0 {
		runner = doc.Plugins[i].Name
	}

	var out []string

	retired := func(key string, tags any, rules ...string) {
		for i, r := range rules {
			rules[i] = "{" + r + ", tags: " + flowList(tags) + "}"
		}

		out = append(out, fmt.Sprintf("%s: placement is the runner's: plugins.%s.config.%s.rules: %s",
			key, runner, PlacementKey, strings.Join(rules, " and ")))
	}

	if tags, ok := doc.Concourse.Planner["tags"]; ok {
		retired("concourse.planner.tags", tags, "for: pipeline", "for: planning")
	}

	// a key left out leaves its node empty, of kind 0
	if n := &doc.Concourse.ResourceTags; n.Kind != 0 {
		retired("concourse.resource_tags", nodeValue(n), "for: checks")
	}

	if n := &doc.Concourse.Bookkeeping; n.Kind != 0 {
		var b map[string]any

		_ = n.Decode(&b) //nolint:errcheck // a bookkeeping that isn't a mapping is named with no tags

		if tags, ok := b["tags"]; ok {
			retired("concourse.bookkeeping.tags", tags, "for: bookkeeping")
		} else {
			out = append(out, "concourse.bookkeeping: placement is the runner's: remove it; without a rule of its own, the bookkeeping runs with its suite")
		}
	}

	for _, name := range slices.Sorted(maps.Keys(doc.Suites)) {
		runOn, _ := doc.Suites[name]["run_on"].(map[string]any) //nolint:errcheck // a run_on that isn't a mapping places nothing
		if tags, ok := runOn["tags"]; ok {
			retired("suites."+name+".run_on.tags", tags, "for: suite, suite: "+name)
		}
	}

	return out
}

type namedPlugin struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

func nodeValue(n *yaml.Node) any {
	var v any

	_ = n.Decode(&v) //nolint:errcheck // a value that doesn't decode is written as nothing

	return v
}

func flowList(v any) string {
	list, _ := v.([]any) //nolint:errcheck // anything but a list is no tags

	tags := make([]string, 0, len(list))
	for _, t := range list {
		tags = append(tags, fmt.Sprint(t))
	}

	return "[" + strings.Join(tags, ", ") + "]"
}

// runnerConfig is the runner's whole config block as canonical JSON for the
// trusted keys, so the same block gives the same bytes however the file
// orders it. Only a config without a runner has none.
func (c *Config) runnerConfig() *json.RawMessage {
	p, ok := c.Runner()
	if !ok {
		return nil
	}

	b := json.RawMessage("{}")

	if len(p.Config) > 0 {
		data, err := json.Marshal(p.Config)
		if err != nil {
			return nil
		}

		b = data
	}

	return &b
}

func (c *Config) runnerConfigKey() string {
	if p, ok := c.Runner(); ok {
		return "plugins." + p.Name + ".config"
	}

	return "plugins.<runner>.config"
}

// trustRunnerConfig gives the runner the trusted config block and names the
// block when the change edits it. The pipeline is made from it, so a
// change's own block takes effect once merged.
func (c *Config) trustRunnerConfig(raw *json.RawMessage, same func(key string, a, b any)) {
	if raw == nil {
		return
	}

	same(c.runnerConfigKey(), c.runnerConfig(), raw)

	var block map[string]any
	if json.Unmarshal(*raw, &block) != nil || len(block) == 0 {
		block = nil
	}

	for i, p := range c.Plugins {
		if p.Kind == KindRunner {
			c.Plugins[i].Config = block
		}
	}
}
