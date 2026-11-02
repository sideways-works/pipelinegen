package cli

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestAProposedChangeTakesTheTrustedKeys(t *testing.T) {
	base, err := config.Parse([]byte(reportConfig))
	if err != nil {
		t.Fatal(err)
	}

	blob, err := json.Marshal(base.Trusted())
	if err != nil {
		t.Fatal(err)
	}

	changed := func() *config.Config {
		c, err := config.Parse([]byte(strings.Replace(reportConfig, "github: {repository: o/r, token: gh}", "github: {repository: o/r, token: gh}\ngit: {remote: \"https://evil.example/r.git\"}", 1)))
		if err != nil {
			t.Fatal(err)
		}

		return c
	}

	pr := runs.Change{Run: "pr", ID: "pr-7", PR: 7}

	t.Setenv(envTrusted, string(blob))

	c := changed()
	if err := trustChange(c, pr); err != nil {
		t.Fatal(err)
	}

	if c.Git.Remote != "" || len(c.Warnings) == 0 || !strings.HasPrefix(c.Warnings[0], "planned with the repository's own git.remote: this change edits them") ||
		!strings.HasSuffix(c.Warnings[0], "see Trying a new pin before merging ("+trialPinDoc+")") {
		t.Fatalf("remote %q, warnings %v", c.Git.Remote, c.Warnings)
	}

	c = changed()
	if err := trustChange(c, runs.Change{Run: "main", ID: "main"}); err != nil || c.Git.Remote == "" {
		t.Fatalf("a trusted run's config was changed: %v %q", err, c.Git.Remote)
	}

	t.Setenv(envTrusted, "")

	c = changed()
	if err := trustChange(c, pr); err != nil || !slices.Contains(c.Warnings, noTrustedKeys) || c.Warnings[0] != noTrustedKeys {
		t.Fatalf("without keys: %v %v", err, c.Warnings)
	}
}

func TestAShallowTrustedRunStillAttaches(t *testing.T) {
	cfg := &config.Config{
		Concourse: config.Concourse{Branch: "main"},
		Runs:      map[string]config.Run{"nightly": {Trigger: config.Trigger{Branch: "main"}}},
	}

	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "r.git")
	gitT(t, work, "init", "-q", "-b", "main")
	gitT(t, work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one")
	gitT(t, work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two")
	gitT(t, t.TempDir(), "clone", "-q", "--bare", work, bare)

	trigger := filepath.Join(t.TempDir(), "trigger")
	gitT(t, t.TempDir(), "clone", "-q", "--depth", "1", "--single-branch", "--branch", "main", "file://"+bare, trigger)

	ch, err := runs.FromTrigger(t.Context(), cfg, "nightly", trigger)
	if err != nil {
		t.Fatal(err)
	}

	if id := reportID(ch); id.Proposed || !attachTrusted(cfg, id) {
		t.Fatalf("a shallow checkout of the tip: %+v", id)
	}
}
