package cli

import (
	"os/exec"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestEveryStoreRetriesAsTheConfigSays(t *testing.T) {
	if s := openStore(&config.Config{}); s.Retries != config.DefaultRetries || s.Backoff != time.Second || s.RetryFor != 10*time.Minute {
		t.Fatalf("defaults: %d %s %s", s.Retries, s.Backoff, s.RetryFor)
	}

	three := 3
	cfg := &config.Config{Writes: config.Writes{Retries: &three, Backoff: "2s", Deadline: "3m"}, Store: config.Store{Backend: config.StoreBranch}}

	s := openStore(cfg)
	if s.Retries != 3 || s.Backoff != 2*time.Second || s.RetryFor != 3*time.Minute || s.Branch.RetryFor != 3*time.Minute {
		t.Fatalf("config: %d %s %s", s.Retries, s.Backoff, s.RetryFor)
	}

	if !s.Until.IsZero() {
		t.Fatalf("a store has an end of its own: %s", s.Until)
	}
}

func TestTheStoreIsTheCodeRemote(t *testing.T) {
	t.Setenv(envGitURL, "")

	gh := &config.Config{GitHub: config.GitHub{Repository: "o/r", Token: "gh"}}
	if s := openStore(gh); s.Remote != "https://github.com/o/r.git" {
		t.Fatalf("host's: %s", s.Remote)
	}

	own := &config.Config{Git: config.Git{Remote: "git://example.test/r.git"}, Store: config.Store{Backend: config.StoreBranch}}
	if s := openStore(own); s.Remote != "git://example.test/r.git" || s.Branch == nil || s.Branch.Remote != s.Remote {
		t.Fatalf("own: %s %+v", s.Remote, s.Branch)
	}

	if _, ok := host(own).(report.Nowhere); !ok {
		t.Fatalf("a repository with no host reports to %T", host(own))
	}

	if _, ok := host(gh).(*report.Client); !ok {
		t.Fatalf("a github repository reports to %T", host(gh))
	}
}

func TestATokenIsReadForItsPurpose(t *testing.T) {
	t.Setenv(envToken, "one")
	t.Setenv("PIPELINEGEN_STORE_TOKEN", "store")
	t.Setenv("PIPELINEGEN_READ_TOKEN", "")

	one := &config.Config{GitHub: config.GitHub{Repository: "o/r", Token: "gh"}}
	if tokenFor(one, "store") != "one" || tokenFor(one, "read") != "one" || openStore(one).Token != "one" {
		t.Fatal("one token")
	}

	split := &config.Config{GitHub: config.GitHub{Repository: "o/r", Token: "gh", Tokens: config.Tokens{Read: "ro"}}}
	if tokenFor(split, "store") != "store" || tokenFor(split, "read") != "" || openStore(split).Token != "store" {
		t.Fatalf("split: store %q read %q", tokenFor(split, "store"), tokenFor(split, "read"))
	}

	if c, ok := host(split).(*report.Client); !ok || c.Token != "" {
		t.Fatalf("the report token is its own variable's, unset here: %+v", host(split))
	}
}

func TestAFetchAuthenticatesToItsOwnHost(t *testing.T) {
	t.Setenv(envToken, "one")

	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://git.example.test/r.git"}} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	gh := &config.Config{GitHub: config.GitHub{Repository: "o/r", Token: "gh"}}
	if a := readAuth(t.Context(), gh, repo); a.Scope != runs.GitHubScope || a.Token != "one" {
		t.Fatalf("github: %+v", a)
	}

	own := &config.Config{Git: config.Git{Remote: "https://git.example.test/r.git"}}
	if a := readAuth(t.Context(), own, repo); a.Scope != "https://git.example.test/r.git" {
		t.Fatalf("own host: %+v", a)
	}
}

func TestTheBackgroundFlushHasNoStepsEnd(t *testing.T) {
	cfg := &config.Config{Store: config.Store{Backend: config.StoreBranch}}

	s := openStore(cfg)
	s.Until = time.Now().Add(time.Minute)
	s.Branch.Until = s.Until

	inBackground(s)

	if s.Retries != 0 || !s.Until.IsZero() || s.Branch.Retries != 0 || !s.Branch.Until.IsZero() {
		t.Fatalf("retries %d, until %s; branch %d, %s", s.Retries, s.Until, s.Branch.Retries, s.Branch.Until)
	}
}
