package config

import (
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
    lock: {name: browsers, minutes: 90, repository: o/locks}
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
		"repository: o/locks}", "repository: evil/locks}",
		"github: {repository: o/r, token: gh}", "github: {repository: o/r, token: gh}\ngit: {remote: \"https://evil.example/r.git\"}",
		`release: {version: v1, sha256: "1111111111111111111111111111111111111111111111111111111111111111"}}`, `release: {version: v2, sha256: "9999999999999999999999999999999999999999999999999999999999999999"}}`,
	).Replace(trustedBase) + "  - {name: extra, kind: correlator, repository: e/extra, release: {version: v1, sha256: \"3333333333333333333333333333333333333333333333333333333333333333\"}}\n"

	c := parseTrusted(t, changed)
	edited := c.WithTrusted(trusted)

	for _, key := range []string{"bazel_diff", "suites.*.lock.repository", "git.remote", "plugins"} {
		if !slices.Contains(edited, key) {
			t.Errorf("%s not named as edited: %v", key, edited)
		}
	}

	switch {
	case c.BazelDiff.Version != "v1.0.0" || c.Git.Remote != "" || c.Suites["e2e"].Lock.Repository != "o/locks":
		t.Errorf("not undone: %+v %q %+v", c.BazelDiff, c.Git.Remote, c.Suites["e2e"].Lock)
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

func TestAChangesEditsOfItsLockAreUndone(t *testing.T) {
	base := strings.Replace(trustedBase, "lock: {name: browsers, minutes: 90, repository: o/locks}",
		"lock: {name: browsers, minutes: 90, repository: o/locks, slots: 2}", 1) +
		"runs:\n  pr:\n    trigger: {pull_requests: {base: main}}\n    compare: merged_target\n    select: affected\n    suites: {e2e: {lock_minutes: 120}}\n"
	trusted := parseTrusted(t, base).Trusted()

	changed := strings.NewReplacer(
		"lock: {name: browsers, minutes: 90, repository: o/locks, slots: 2}", "lock: {name: mine, minutes: 900, repository: o/locks, slots: 9, per: batch, weight: 9}",
		"{e2e: {lock_minutes: 120}}", "{e2e: {lock_minutes: 1200}}",
	).Replace(base)

	c := parseTrusted(t, changed)
	if edited := c.WithTrusted(trusted); !slices.Equal(edited, []string{"suites.*.lock"}) {
		t.Errorf("edited: %v, want only suites.*.lock", edited)
	}

	want := Lock{Name: "browsers", Repository: "o/locks", Slots: 2}
	if got := *c.Suites["e2e"].Lock; got != want {
		t.Errorf("lock %+v, want %+v", got, want)
	}

	if got, run := c.SuiteMinutes("", "e2e"), c.SuiteMinutes("pr", "e2e"); got != 90 || run != 120 {
		t.Errorf("minutes %d, in the run %d, want 90 and 120", got, run)
	}

	raised := parseTrusted(t, strings.NewReplacer(
		"    lock: {name: browsers, minutes: 90, repository: o/locks, slots: 2}\n", "    minutes: 900\n    lock: {name: browsers, repository: o/locks, slots: 2}\n",
		"{e2e: {lock_minutes: 120}}", "{e2e: {minutes: 1200}}",
	).Replace(base))

	if edited := raised.WithTrusted(trusted); !slices.Equal(edited, []string{"suites.*.lock"}) {
		t.Errorf("raised minutes: edited %v", edited)
	}

	if got, run := raised.SuiteMinutes("", "e2e"), raised.SuiteMinutes("pr", "e2e"); got != 90 || run != 120 {
		t.Errorf("raised minutes: %d, in the run %d, want 90 and 120", got, run)
	}

	added := parseTrusted(t, strings.Replace(trustedBase, "    lock: {name: browsers, minutes: 90, repository: o/locks}\n", "", 1)).Trusted()

	c = parseTrusted(t, changed)
	c.WithTrusted(added)

	if c.Suites["e2e"].Lock != nil {
		t.Errorf("a lock the trusted config lacks: %+v", c.Suites["e2e"].Lock)
	}

	old := trusted
	old.Locks = nil

	c = parseTrusted(t, changed)
	if edited := c.WithTrusted(old); len(edited) != 0 {
		t.Errorf("edited: %v", edited)
	}

	if l := c.Suites["e2e"].Lock; l.Slots != 9 || l.Repository != "o/locks" {
		t.Errorf("an older tool's trusted keys changed the lock: %+v", l)
	}
}
