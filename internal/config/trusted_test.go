package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const trustedBase = `pipeline: shop
bazel_diff: {version: v1.0.0, sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    run_slice: ci/e2e.yml
github: {repository: o/r, token: gh}
plugins:
  - {name: go-sbom, kind: sbom, repository: s/go-sbom, release: {version: v1, sha256: "1111111111111111111111111111111111111111111111111111111111111111"}}
  - {name: go-correlator, kind: correlator, repository: s/go-correlator, release: {version: v1, sha256: "2222222222222222222222222222222222222222222222222222222222222222"}}
`

func parseTrusted(t *testing.T, text string) *Config {
	t.Helper()

	c, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestTheSameValueWrittenOtherwiseIsNoEdit(t *testing.T) {
	trusted := parseTrusted(t, trustedBase).Trusted()

	other := strings.NewReplacer(
		"github: {repository: o/r, token: gh}", "github: {token: gh, repository: o/r, status_prefix: pipelinegen}",
	).Replace(trustedBase)

	lines := strings.Split(other, "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "go-sbom") })
	lines[i], lines[i+1] = lines[i+1], lines[i]

	c := parseTrusted(t, strings.Join(lines, "\n"))
	if edited := c.WithTrusted(trusted); len(edited) != 0 {
		t.Fatalf("edited: %v", edited)
	}
}

func TestARepositorysOtherSpellingIsNoEdit(t *testing.T) {
	trusted := parseTrusted(t, trustedBase).Trusted()

	for repository, edit := range map[string]bool{
		"s/pipelinegen-sbom-go":       false,
		"evil/pipelinegen-sbom-go":    true,
		"evil/go-sbom":                true,
		"s/pipelinegen-correlator-go": true,
		"s/pipelinegen-go-sbom":       true,
	} {
		c := parseTrusted(t, strings.Replace(trustedBase, "repository: s/go-sbom,", "repository: "+repository+",", 1))

		if edited := c.WithTrusted(trusted); slices.Contains(edited, "plugins") != edit {
			t.Errorf("%s: edited %v, want plugins edited %v", repository, edited, edit)
		}

		if c.Plugins[0].Repository != "s/go-sbom" {
			t.Errorf("%s: the change's repository stands: %+v", repository, c.Plugins[0])
		}
	}
}

func TestAChangesEditsOfTrustedKeysAreUndone(t *testing.T) {
	trusted := parseTrusted(t, trustedBase).Trusted()

	changed := strings.NewReplacer(
		"bazel_diff: {version: v1.0.0, sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}", "bazel_diff: {version: v9.9.9, sha256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}",
		"github: {repository: o/r, token: gh}", "github: {repository: o/r, token: gh}\ngit: {remote: \"https://evil.example/r.git\"}",
		`release: {version: v1, sha256: "1111111111111111111111111111111111111111111111111111111111111111"}}`, `release: {version: v2, sha256: "9999999999999999999999999999999999999999999999999999999999999999"}}`,
	).Replace(trustedBase) + "  - {name: extra, kind: correlator, repository: e/extra, release: {version: v1, sha256: \"3333333333333333333333333333333333333333333333333333333333333333\"}}\n"

	c := parseTrusted(t, changed)
	edited := c.WithTrusted(trusted)

	for _, key := range []string{"bazel_diff", "git.remote", "plugins"} {
		if !slices.Contains(edited, key) {
			t.Errorf("%s not named as edited: %v", key, edited)
		}
	}

	switch {
	case c.BazelDiff.Version != "v1.0.0" || c.Git.Remote != "":
		t.Errorf("not undone: %+v %q", c.BazelDiff, c.Git.Remote)
	case len(c.Plugins) != 2 || c.Plugins[0].Release.Version != "v1":
		t.Errorf("plugins: %+v", c.Plugins)
	}
}

func TestAChangeCantTakeASuiteOutOfItsRun(t *testing.T) {
	const runs = `runs:
  pr: {trigger: {pull_requests: {base: main}}, compare: merged_target, select: affected, suites: SUITES}
`

	twoSuites := strings.Replace(trustedBase, "suites:\n", "suites:\n  unit:\n    tests: [\"unit/*_test.go\"]\n    run_slice: ci/unit.yml\n", 1)
	with := func(suites string) *Config {
		return parseTrusted(t, twoSuites+strings.Replace(runs, "SUITES", suites, 1))
	}

	trusted := with("{e2e: {}, unit: {}}").Trusted()

	took := with("{e2e: {}}")
	if edited := took.WithTrusted(trusted); !slices.Equal(edited, []string{"runs.pr.suites"}) || !slices.Equal(took.Runs["pr"].RunSuiteNames(), []string{"e2e", "unit"}) {
		t.Errorf("a suite taken out: %v %v", edited, took.Runs["pr"].RunSuiteNames())
	}

	added := with("{e2e: {}, unit: {}}")
	if edited := added.WithTrusted(with("{e2e: {}}").Trusted()); len(edited) != 0 || len(added.Runs["pr"].Suites) != 2 {
		t.Errorf("a suite added: %v %v", edited, added.Runs["pr"].RunSuiteNames())
	}

	older := trusted
	older.RunSuites = nil

	untold := with("{e2e: {}}")
	if edited := untold.WithTrusted(older); len(edited) != 0 || len(untold.Runs["pr"].Suites) != 1 {
		t.Errorf("an older pipeline's keys: %v %v", edited, untold.Runs["pr"].RunSuiteNames())
	}
}

func TestAnOlderPipelinesTrustedLocksAreNoEdit(t *testing.T) {
	data, err := json.Marshal(parseTrusted(t, trustedBase).Trusted())
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Replace(string(data), "{", `{"lock_repositories":{"e2e":"o/locks"},"locks":{"suites":{"e2e":{"name":"browsers","minutes":90,"slots":2}}},`, 1)

	var trusted Trusted
	if err := json.Unmarshal([]byte(old), &trusted); err != nil {
		t.Fatal(err)
	}

	c := parseTrusted(t, strings.Replace(trustedBase, "    run_slice: ci/e2e.yml\n", "    run_slice: ci/e2e.yml\n    lock: {name: mine, slots: 9, per: batch}\n", 1))
	if edited := c.WithTrusted(trusted); len(edited) != 0 {
		t.Errorf("edited: %v", edited)
	}
}
