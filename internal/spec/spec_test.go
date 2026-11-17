package spec

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestFromFixture(t *testing.T) {
	cfg, err := config.Load("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if s.Pipeline != "fixture" || s.GitHub.TokenVar != "github_token" || s.Branch != "main" {
		t.Fatalf("spec %+v", s)
	}

	kinds := map[string]string{}
	for _, r := range s.Runs {
		kinds[r.Name] = r.Trigger.Kind
	}

	if kinds["pr"] != "pull_requests" || kinds["main"] != "branch" || kinds["nightly"] != "timer" {
		t.Fatalf("triggers %v", kinds)
	}

	for _, r := range s.Runs {
		for _, rs := range r.Suites {
			if r.Name == "nightly" && rs.Suite == "e2e" && (rs.Params["PROJECTS"] != "all" || !rs.Records) {
				t.Fatalf("nightly e2e %+v", rs)
			}
		}
	}

	if s.CI["url"] != "https://ci.example" || s.Tools[0].Commit == "" || s.Tools[1].Kind != "bazelisk" {
		t.Fatalf("ci %v tools %+v", s.CI, s.Tools)
	}

	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
}

func TestASpecNeedsTheConcourseURL(t *testing.T) {
	cfg := &config.Config{Runs: map[string]config.Run{"main": {}}}

	if _, err := From(cfg); !errors.Is(err, ErrSpec) {
		t.Fatalf("no url: %v", err)
	}
}

func TestASpecNeedsWhereTheRecordsLive(t *testing.T) {
	base, err := os.ReadFile("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	text := strings.Replace(string(base), "github: {repository: owner/fixture, token: github_token}\n", "", 1)
	if text == string(base) {
		t.Fatal("the fixture changed")
	}

	cfg, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := From(cfg); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), "git.remote") {
		t.Fatalf("no remote: %v", err)
	}

	cfg.Git.Remote = "https://git.example.test/fixture.git"

	if _, err := From(cfg); err == nil || !strings.Contains(err.Error(), "a changes (pull_requests) trigger needs a host or a connector") {
		t.Fatalf("pull requests without a host: %v", err)
	}
}

func TestTokensReachTheRunnerByPurpose(t *testing.T) {
	base, err := os.ReadFile("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(base)
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(s.GitHub)
	if err != nil {
		t.Fatal(err)
	}

	if s.GitHub.TokenVar != "github_token" || strings.Contains(string(data), "tokens") {
		t.Fatalf("one token: %s", data)
	}

	text := strings.Replace(string(base), "token: github_token}", "token: github_token, tokens: {read: read_token}}", 1)
	if text == string(base) {
		t.Fatal("the fixture changed")
	}

	if cfg, err = config.Parse([]byte(text)); err != nil {
		t.Fatal(err)
	}

	if s, err = From(cfg); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"read": "read_token", "changes": "github_token", "report": "github_token", "store": "github_token"}
	if !reflect.DeepEqual(s.GitHub.Tokens, want) || s.GitHub.TokenVar != "github_token" {
		t.Fatalf("split: %+v", s.GitHub)
	}
}

func TestSpecCarriesNoWorkerTags(t *testing.T) {
	cfg, err := config.Load("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		CI     map[string]any   `json:"ci"`
		Suites []map[string]any `json:"suites"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	planner, _ := doc.CI["planner"].(map[string]any) //nolint:errcheck // a missing planner has no tags either

	for _, key := range []string{"resource_tags", "bookkeeping"} {
		if _, ok := doc.CI[key]; ok {
			t.Errorf("the concourse section has %s", key)
		}
	}

	if _, ok := planner["tags"]; ok {
		t.Error("the planner has tags")
	}

	for _, su := range doc.Suites {
		if _, ok := su["tags"]; ok {
			t.Errorf("suite %v has tags", su["name"])
		}
	}
}

func TestImagesReachTheRunner(t *testing.T) {
	cfg, err := config.Load("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	planner, _ := s.CI["planner"].(map[string]any) //nolint:errcheck // checked below
	if _, has := planner["prepare_image"]; has || planner == nil {
		t.Fatalf("left out, the planner %v", planner)
	}

	for _, su := range s.Suites {
		if su.Image != "" {
			t.Fatalf("left out, %s has image %q", su.Name, su.Image)
		}
	}

	e2e := cfg.Suites["e2e"]
	e2e.Image = "node-image"
	cfg.Suites["e2e"] = e2e
	cfg.Concourse.Planner.PrepareImage = "go-image"

	if s, err = From(cfg); err != nil {
		t.Fatal(err)
	}

	planner, _ = s.CI["planner"].(map[string]any) //nolint:errcheck // checked below
	if planner["prepare_image"] != "go-image" {
		t.Fatalf("the planner %v", planner)
	}

	for _, su := range s.Suites {
		if want := map[string]string{"e2e": "node-image"}[su.Name]; su.Image != want {
			t.Fatalf("%s: image %q", su.Name, su.Image)
		}
	}
}

func TestTheRunnerLearnsTheDraftState(t *testing.T) {
	cfg, err := config.Load("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	pr := cfg.Runs["pr"].Trigger.PullRequests

	for draft, want := range map[config.Drafts]string{"": "false", config.DraftsOnly: "true", config.DraftsAny: "<nil>"} {
		pr.Draft = draft

		s, err := From(cfg)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range s.Runs {
			if r.Name != "pr" {
				continue
			}

			got := "<nil>"
			if r.Trigger.Draft != nil {
				got = strconv.FormatBool(*r.Trigger.Draft)
			}

			if got != want {
				t.Errorf("draft %q: the spec says %s, want %s", draft, got, want)
			}
		}
	}
}

// A config's github section runs the pinned GitHub connector, so the spec
// names it for the bootstrap to fetch; a plugins entry by its name pins
// another in its place.
func TestTheSpecFetchesTheConnector(t *testing.T) {
	base, err := os.ReadFile("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	connectors := func(text string) []plugin.SpecTool {
		cfg, err := config.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}

		s, err := From(cfg)
		if err != nil {
			t.Fatal(err)
		}

		var out []plugin.SpecTool

		for _, tool := range s.Tools {
			if tool.Kind == config.KindConnector {
				out = append(out, tool)
			}
		}

		return out
	}

	pin := config.PinnedConnectors[config.ConnectorGitHub]
	if got := connectors(string(base)); len(got) != 1 || got[0].Name != "github" || got[0].Repository != pin.Repository || got[0].Version != pin.Version {
		t.Fatalf("%+v", got)
	}

	sum := strings.Repeat("cd", 32)
	entry := "plugins:\n  - {name: github, kind: connector, repository: team/pipelinegen-connector-github, release: {version: v9.0.0, sha256: " + sum + "}}\n"

	if got := connectors(strings.Replace(string(base), "plugins:\n", entry, 1)); len(got) != 1 || got[0].Version != "v9.0.0" || got[0].SHA256 != sum {
		t.Fatalf("%+v", got)
	}
}

func TestAnOldLockReachesNoRunner(t *testing.T) {
	data, err := os.ReadFile("../../testdata/kit/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	text := strings.Replace(string(data), "    prepare: ci/e2e-prepare.yml\n", "    prepare: ci/e2e-prepare.yml\n    lock: {name: browsers, minutes: 90, slots: 2, per: batch}\n", 1)
	if text == string(data) {
		t.Fatal("the fixture moved")
	}

	cfg, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), `"lock`) {
		t.Errorf("the spec holds a lock: %s", out)
	}

	for _, r := range s.Runs {
		for _, rs := range r.Suites {
			if rs.Suite == "e2e" && rs.Minutes != 90 {
				t.Errorf("run %s: e2e's limit %d, want lock.minutes, the old spelling of minutes, 90", r.Name, rs.Minutes)
			}
		}
	}
}
