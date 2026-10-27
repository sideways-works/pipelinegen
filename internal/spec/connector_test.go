package spec

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestAConnectorsRunTellsTheRunnerItsConnector(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	github := string(data)
	connector := strings.NewReplacer(
		"github: {repository: owner/fixture, token: github_token}",
		"git: {remote: \"https://example.test/r.git\"}\nconnectors:\n  - {name: git, plugin: git, changes: {branches: [\"review/*\"]}}",
		"trigger: {pull_requests: {base: main, draft: false, labels_without: [batch]}}", "trigger: {changes: {base: main, SWEEP}}",
		"    report: {comment: {format: mermaid}}\n", "",
	).Replace(github)

	for text, want := range map[string][2]string{
		github: {"", ""},
		strings.Replace(connector, ", SWEEP", "", 1):              {"git", "1m"},
		strings.Replace(connector, "SWEEP", "sweep_every: 5m", 1): {"git", "5m"},
	} {
		cfg, err := config.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}

		s, err := From(cfg)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range s.Runs {
			if r.Name != "pr" {
				continue
			}

			if got := [2]string{r.Trigger.Connector, r.Trigger.SweepEvery}; got != want {
				t.Errorf("connector and sweep %q, want %q", got, want)
			}
		}
	}
}

func TestTheRunnerLearnsTheSSHKeysVars(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	ssh := strings.NewReplacer(
		"github: {repository: owner/fixture, token: github_token}",
		"git: {remote: \"ssh://git@git.example.test/r.git\", ssh_key: {read: ssh_read, store: ssh_store}, known_hosts: [\"git.example.test ssh-ed25519 AAAAkey\"]}\nconnectors:\n  - {name: git, plugin: git, changes: {branches: [\"review/*\"]}}",
		"trigger: {pull_requests: {base: main, draft: false, labels_without: [batch]}}", "trigger: {changes: {base: main}}",
		"    report: {comment: {format: mermaid}}\n", "",
	).Replace(string(data))

	for text, want := range map[string]string{string(data): "map[]", ssh: "map[read:ssh_read store:ssh_store]"} {
		cfg, err := config.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}

		s, err := From(cfg)
		if err != nil {
			t.Fatal(err)
		}

		if got := fmt.Sprint(s.SSHKeys); got != want {
			t.Errorf("ssh keys %s, want %s", got, want)
		}
	}
}
