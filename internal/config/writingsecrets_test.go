package config

import (
	"strings"
	"testing"
)

const writingBase = `pipeline: p
github: {repository: o/r, TOKENS}
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/e2e-slice.yml
    prepare: ci/e2e-prepare.yml
    secrets: {prepare: {SECRET}}
runs:
  pr:
    trigger: {pull_requests: {base: main}}
    compare: merged_target
    select: affected
    suites: {e2e: {}}
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {e2e: {}}
`

func writing(tokens, secret string, branchOnly bool) string {
	text := strings.NewReplacer("TOKENS", tokens, "SECRET", secret).Replace(writingBase)
	if branchOnly {
		text = strings.Replace(text, "  pr:\n    trigger: {pull_requests: {base: main}}\n    compare: merged_target\n    select: affected\n    suites: {e2e: {}}\n", "", 1)
	}

	return text
}

func TestATokenThatWritesIsNoSecretOfAPullRequestsTask(t *testing.T) {
	const line = "task e2e/prepare is given gh, a token that can write, in a run that executes a change's own scripts: give it a token that only reads (github.tokens.read)"

	const split = "tokens: {read: r, changes: c, report: p, store: s}"

	cfg, err := Parse([]byte(writing("token: gh", "GITHUB_TOKEN: gh", false)))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.WritingSecrets("pr"); len(got) != 1 || got[0] != line {
		t.Errorf("one token, a pull request's run: %q", got)
	}

	if got := cfg.WritingSecrets("main"); len(got) != 0 {
		t.Errorf("a branch's run: %q", got)
	}

	if cfg, err := Parse([]byte(writing("token: gh", "GITHUB_TOKEN: gh", true))); err != nil || len(cfg.WritingSecrets("")) != 0 {
		t.Errorf("branch runs only: %v %v", err, cfg)
	}

	_, err = Parse([]byte(writing(split, "STORE: s", false)))
	if err == nil || !strings.Contains(err.Error(), "task e2e/prepare is given s, a token that can write") {
		t.Errorf("tokens by purpose, the store's token: %v", err)
	}

	for _, secret := range []string{"GITHUB_TOKEN: r", "NPM_TOKEN: npm"} {
		if cfg, err := Parse([]byte(writing(split, secret, false))); err != nil || len(cfg.WritingSecrets("")) != 0 {
			t.Errorf("tokens by purpose, %s: %v", secret, err)
		}
	}
}
