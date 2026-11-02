package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestAProposedChangesStepHoldsTheReadTokenAlone(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "PIPELINEGEN_READ_TOKEN", "PIPELINEGEN_STORE_TOKEN", "PIPELINEGEN_REPORT_TOKEN", "PIPELINEGEN_CHANGES_TOKEN", "PIPELINEGEN_SSH_KEY_STORE"} {
		t.Setenv(name, "")
	}

	split := &config.Config{}
	split.GitHub.Token, split.GitHub.Tokens.Read = "gh", "gh_read"

	t.Setenv("PIPELINEGEN_READ_TOKEN", "read")

	if lines, err := guardProposed(split); err != nil || len(lines) != 0 {
		t.Fatalf("the read token alone: %q %v", lines, err)
	}

	for _, name := range []string{"PIPELINEGEN_STORE_TOKEN", "PIPELINEGEN_REPORT_TOKEN", "PIPELINEGEN_CHANGES_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "writes")

			if _, err := guardProposed(split); !errors.Is(err, errWritingCredential) || !strings.Contains(err.Error(), name) {
				t.Fatalf("%v", err)
			}
		})
	}

	one := &config.Config{}
	one.GitHub.Token = "gh"

	t.Setenv("GITHUB_TOKEN", "writes")

	if lines, err := guardProposed(one); err != nil || len(lines) != 1 || lines[0] != singleTokenText {
		t.Fatalf("one token: %q %v", lines, err)
	}

	declared := &config.Config{}
	declared.GitHub.Token, declared.GitHub.Tokens.Read = "gh", "gh_read"
	declared.Concourse.Planner.Secrets = map[string]string{"GITHUB_TOKEN": "gh_read"}
	declared.Concourse.Planner.GitToken = "GITHUB_TOKEN"

	if lines, err := guardProposed(declared); err != nil || len(lines) != 1 ||
		lines[0] != "the planning task holds GITHUB_TOKEN as its declared git credential: make sure it can only read" {
		t.Fatalf("declared: %q %v", lines, err)
	}

	t.Run("declared and a writing one", func(t *testing.T) {
		t.Setenv("PIPELINEGEN_STORE_TOKEN", "writes")

		if _, err := guardProposed(declared); !errors.Is(err, errWritingCredential) || !strings.Contains(err.Error(), "PIPELINEGEN_STORE_TOKEN") {
			t.Fatalf("%v", err)
		}
	})

	keys := &config.Config{}
	keys.Git.SSHKey = config.SSHKeys{Read: "read_key", Store: "store_key"}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PIPELINEGEN_SSH_KEY_STORE", "key")

	if _, err := guardProposed(keys); !errors.Is(err, errWritingCredential) || !strings.Contains(err.Error(), "PIPELINEGEN_SSH_KEY_STORE") {
		t.Fatalf("the store's key: %v", err)
	}
}

func TestTheCheckWarnsOfOneTokenForPullRequests(t *testing.T) {
	cfg := &config.Config{Runs: map[string]config.Run{"pr": {Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}}}}
	cfg.GitHub.Token = "gh"

	got := singleTokenFinding(cfg)
	if len(got) != 1 || got[0].Kind != check.KindBadConfig || !got[0].Warning || got[0].Problem != singleTokenText {
		t.Fatalf("one token: %+v", got)
	}

	cfg.GitHub.Tokens.Read = "gh_read"
	if got := singleTokenFinding(cfg); len(got) != 0 {
		t.Fatalf("tokens by purpose: %+v", got)
	}

	cfg.Concourse.Planner.GitToken = "GITHUB_TOKEN"
	cfg.Concourse.Planner.Secrets = map[string]string{"GITHUB_TOKEN": "gh_read"}

	if got := singleTokenFinding(cfg); len(got) != 1 || !got[0].Note || len(check.Failures(got)) != 0 || !strings.Contains(got[0].Problem, "make sure it can only read") {
		t.Fatalf("declared: %+v", got)
	}

	cfg.Concourse.Planner = config.Planner{}

	cfg.GitHub.Tokens.Read = ""
	cfg.Runs = map[string]config.Run{"main": {Trigger: config.Trigger{Branch: "main"}}}

	if got := singleTokenFinding(cfg); len(got) != 0 {
		t.Fatalf("no pull request run: %+v", got)
	}
}

func TestAPullRequestsPlanSaysFirstThatATaskHoldsAWritingToken(t *testing.T) {
	cfg := loadConfigText(t, strings.Replace(reportConfig,
		"    run_slice: ci/tasks/e2e-slice.yml\n",
		"    run_slice: ci/tasks/e2e-slice.yml\n    prepare: ci/tasks/e2e-prepare.yml\n    secrets: {prepare: {GITHUB_TOKEN: gh}}\n", 1)+
		"  main:\n    trigger: {branch: main}\n    compare: last_passed\n    select: affected\n    suites: {e2e: {}}\n")

	const first = "task e2e/prepare is given gh, a token that can write, in a run that executes a change's own scripts: " +
		"give it a token that only reads (github.tokens.read); the next release refuses it"

	if got := firstWarnings(cfg, "pr", []string{singleTokenText}); len(got) != 2 || got[0] != first || got[1] != singleTokenText {
		t.Fatalf("the pull request run's first warnings: %q", got)
	}

	if got := firstWarnings(cfg, "main", nil); len(got) != 0 {
		t.Errorf("the branch's run: %q", got)
	}

	if got := writingSecretFindings(cfg); len(got) != 1 || !got[0].Warning || got[0].Problem != first {
		t.Errorf("the check: %+v", got)
	}

	if got := firstWarnings(loadConfigText(t, reportConfig), "pr", nil); len(got) != 0 {
		t.Errorf("no such secret: %q", got)
	}
}

func loadConfigText(t *testing.T, text string) *config.Config {
	t.Helper()

	cfg, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}
