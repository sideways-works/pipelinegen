package config

import (
	"reflect"
	"strings"
	"testing"
)

var withGit = strings.NewReplacer(
	"trigger: {branch: main}", "trigger: {changes: {base: main}}",
	"compare: last_passed", "compare: merged_target",
).Replace(withoutHost) + "forges:\n  - {name: git, plugin: git, changes: {branches: [\"review/*\"]}}\n"

func TestThePlainGitForgeListsChanges(t *testing.T) {
	c, err := Parse([]byte(withGit))
	if err != nil {
		t.Fatal(err)
	}

	if err := c.ValidatePipeline(); err != nil {
		t.Fatalf("making the pipeline: %v", err)
	}

	for text, want := range map[string]string{
		strings.Replace(withGit, "{changes: {base: main}}", "{changes: {base: main, sweep_every: 30s}}", 1):          "sweep_every needs a period of a minute or more",
		strings.Replace(withGit, "{changes: {base: main}}", "{changes: {base: main, sweep_every: 5m}}", 1):           "",
		strings.Replace(withGit, `concourse: {url: "https://ci",`, `concourse: {mock: maybe, url: "https://ci",`, 1): `concourse.mock is "maybe"`,
	} {
		c, err := Parse([]byte(text))
		if err == nil {
			err = c.ValidatePipeline()
		}

		if (want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}

	if !c.ChangesFromForge() || c.CodeForge().Name != "git" || c.Reports() {
		t.Fatalf("code forge %+v, reports %v", c.CodeForge(), c.Reports())
	}

	taking := 0

	for _, r := range c.Runs {
		if r.Trigger.Changes != nil {
			t.Fatalf("changes kept beside pull_requests: %+v", r.Trigger)
		}

		if pr := r.Trigger.PullRequests; pr != nil && pr.Base == "main" {
			taking++
		}
	}

	if taking != 1 {
		t.Fatalf("%d runs take changes", taking)
	}

	alias, err := Parse([]byte(strings.Replace(withGit, "trigger: {changes: {base: main}}", "trigger: {pull_requests: {base: main}}", 1)))
	if err != nil {
		t.Fatal(err)
	}

	for name, r := range alias.Runs {
		if !reflect.DeepEqual(r, c.Runs[name]) {
			t.Errorf("run %s differs: %+v and %+v", name, r.Trigger, c.Runs[name].Trigger)
		}
	}
}

func TestThePlainGitForgeIsRefusedWhatItCannotDo(t *testing.T) {
	cases := map[string][3]string{
		"both trigger keys":  {"trigger: {changes: {base: main}}", "trigger: {changes: {base: main}, pull_requests: {base: main}}", "both changes and pull_requests"},
		"no branches":        {`changes: {branches: ["review/*"]}`, "changes: {}", `changes.branches is required: the branches that are proposed changes, e.g. changes: {branches: ["review/*"]}`},
		"a ref in a pattern": {`["review/*"]`, `["refs/heads/review/*"]`, "without refs/heads/"},
		"labels":             {"{changes: {base: main}}", "{changes: {base: main, labels: [ready]}}", "changes.labels needs a forge that tells labels; git (git) can't"},
		"drafts only":        {"{changes: {base: main}}", "{changes: {base: main, draft: true}}", "changes.draft needs a forge that tells draft"},
		"once per ready":     {"{changes: {base: main}}", "{changes: {base: main, once_per_ready: true}}", "changes.once_per_ready needs a forge that tells ready_since"},
		"a comment":          {"compare: merged_target", "compare: merged_target\n    report: {comment: {format: list}}", "a comment needs a forge that keeps one"},
		"a status":           {"compare: merged_target", "compare: merged_target\n    report: {status: true}", "`pipelinegen status` reads the run's state"},
		"another plugin":     {"plugin: git,", "plugin: gitlab,", `plugin "gitlab": forges takes the git forge`},
		"a bad name":         {"name: git,", "name: Git_1,", "name must be lower case"},
		"a bad remote":       {"plugin: git,", "plugin: git, remote: \"https://u:p@example.test/r.git\",", "no credential"},
		"bad links":          {"plugin: git,", "plugin: git, links: \"cgit/{commit}\",", "links must be an http(s) URL"},
		"the github section": {"git: {remote: \"git://example.test/r.git\"}", "github: {repository: o/r, token: gh}", "forges and the github section both"},
	}

	for name, c := range cases {
		text := strings.Replace(withGit, c[0], c[1], 1)
		if text == withGit {
			t.Fatalf("%s: nothing replaced", name)
		}

		_, err := Parse([]byte(text))
		if err == nil || !strings.Contains(err.Error(), c[2]) {
			t.Errorf("%s: %v", name, err)
		}
	}

	twice := withGit + "  - {name: git, plugin: git, changes: {branches: [\"x/*\"]}}\n"
	if _, err := Parse([]byte(twice)); err == nil || !strings.Contains(err.Error(), "another forge has this name") {
		t.Errorf("two forges named alike: %v", err)
	}
}

func TestAForgeIsPinnedLikeAPlugin(t *testing.T) {
	c, err := Parse([]byte(withGit))
	if err != nil {
		t.Fatal(err)
	}

	if pin := c.ForgePin(ForgeGit); !reflect.DeepEqual(pin, PinnedForges[ForgeGit]) {
		t.Fatalf("the default: %+v", pin)
	}

	sum := strings.Repeat("ab", 32)
	entry := "plugins:\n  - {name: git, kind: forge, repository: team/pipelinegen-forge-git, release: {version: v0.3.0, sha256: " + sum + "}}\n"

	c, err = Parse([]byte(withGit + entry))
	if err != nil {
		t.Fatal(err)
	}

	if pin := c.ForgePin(ForgeGit); pin.Repository != "team/pipelinegen-forge-git" || pin.Version != "v0.3.0" || pin.SHA256[anyPlatform] != sum {
		t.Fatalf("the entry's: %+v", pin)
	}

	for text, want := range map[string]string{
		strings.Replace(entry, "name: git", "name: gitlab", 1):                                                       "pins the github or git forge",
		strings.Replace(entry, "release: {version: v0.3.0, sha256: "+sum+"}", "commit: "+strings.Repeat("a", 40), 1): "needs a release",
		strings.Replace(entry, "}}", "}, config: {api: \"https://elsewhere.test\"}}", 1):                             "takes no config",
	} {
		if _, err := Parse([]byte(withGit + text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestAnotherSystemsKindsAndTheOldSectionAreRefused(t *testing.T) {
	for bad, says := range map[string]string{
		"plugins:\n  - {name: github, kind: connector, repository: o/x, commit: " + strings.Repeat("0", 40) + "}\n": "kind connector is not a pipelinegen plugin kind; pipelinegen's is forge",
		"plugins:\n  - {name: lint, kind: step, repository: o/x, commit: " + strings.Repeat("0", 40) + "}\n":        `kind "step" is not a pipelinegen plugin kind`,
		"connectors:\n  - {name: code, plugin: git, remote: \"https://git.example/r.git\"}\n":                       "connectors is now forges, as the plugin kind connector is now forge",
	} {
		_, err := Parse([]byte(withRuns + bad))
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: got %v, want %q", bad, err, says)
		}
	}
}
