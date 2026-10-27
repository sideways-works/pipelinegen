package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestOwnConfigMovedSelectsTheSame(t *testing.T) {
	before, err := os.ReadFile(filepath.Join("testdata", "own", "before.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := config.Parse(before); err == nil || strings.Count(err.Error(), "placement is the runner's") != 8 {
		t.Fatalf("the config before the move: %v", err)
	}

	stripped, err := config.Parse(withoutRetired(t, before))
	if err != nil {
		t.Fatal(err)
	}

	after, err := config.Load(filepath.Join("testdata", "own", "after.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	runner, ok := after.Runner()
	if !ok || runner.Config[config.PlacementKey] == nil {
		t.Fatal("the config after the move has no placement")
	}

	withoutBlock := *after
	withoutBlock.Plugins = append([]config.Plugin(nil), after.Plugins...)

	for i := range withoutBlock.Plugins {
		if withoutBlock.Plugins[i].Kind == config.KindRunner {
			withoutBlock.Plugins[i].Config = nil
		}
	}

	if !reflect.DeepEqual(stripped, &withoutBlock) {
		t.Fatal("the moved config differs from the one before in more than the runner's block")
	}

	t.Setenv(config.EnvConcourseURL, "https://ci.example")

	sb, err := From(stripped)
	if err != nil {
		t.Fatal(err)
	}

	sa, err := From(after)
	if err != nil {
		t.Fatal(err)
	}

	var tb, ta map[string]any
	if json.Unmarshal([]byte(sb.Trusted), &tb) != nil || json.Unmarshal([]byte(sa.Trusted), &ta) != nil {
		t.Fatal("trusted keys that aren't JSON")
	}

	if b, _ := tb["runner_config"].(map[string]any); len(b) > 0 || ta["runner_config"] == nil { //nolint:errcheck // anything but a mapping is no block
		t.Errorf("the runner's block in the trusted keys: before %v, after %v", tb["runner_config"], ta["runner_config"])
	}

	delete(ta, "runner_config")
	delete(tb, "runner_config")

	if !reflect.DeepEqual(ta, tb) {
		t.Error("the trusted keys differ in more than the runner's block")
	}

	sa.Trusted, sb.Trusted = "", ""

	if !reflect.DeepEqual(sa, sb) {
		t.Error("the spec differs in more than the trusted keys")
	}

	for _, s := range sa.Suites {
		if !reflect.DeepEqual(s, plugin.SpecSuite{Name: s.Name, Prepare: s.Prepare, RunSlice: s.RunSlice, Image: s.Image,
			SerialGroup: s.SerialGroup, MaxInFlight: s.MaxInFlight, Parallel: s.Parallel, Privileged: s.Privileged,
			Lock: s.Lock, WholeSuite: s.WholeSuite}) {
			t.Errorf("suite %s carries more than the spec's fields", s.Name)
		}
	}
}

func withoutRetired(t *testing.T, text []byte) []byte {
	t.Helper()

	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}

	ci, _ := doc["concourse"].(map[string]any) //nolint:errcheck // the test's own fixture has one
	delete(ci, "resource_tags")
	delete(ci, "bookkeeping")

	if planner, ok := ci["planner"].(map[string]any); ok {
		delete(planner, "tags")
	}

	suites, _ := doc["suites"].(map[string]any) //nolint:errcheck // the test's own fixture has them
	for _, s := range suites {
		su, _ := s.(map[string]any) //nolint:errcheck // a suite is a mapping
		if runOn, ok := su["run_on"].(map[string]any); ok {
			delete(runOn, "tags")

			if len(runOn) == 0 {
				delete(su, "run_on")
			}
		}
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	return out
}
