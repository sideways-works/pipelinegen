package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The target moved on between the two steps: a plan merged into the older tip is still this change's,
// and one against a tip off the target's history is not.
func TestASecondStepTakesAPlanAgainstAnOlderTip(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)
	cfg := loadConfig(t, root)

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), cfg, ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	remote := strings.TrimPrefix(s.Remote, "file://")
	work := t.TempDir()

	for _, args := range [][]string{
		{"clone", "-q", "-b", "main", remote, work},
		{"-C", work, "-c", "user.name=a", "-c", "user.email=a@a", "commit", "-q", "--allow-empty", "-m", "moved on"},
		{"-C", work, "push", "-q", "origin", "HEAD:refs/heads/main"},
	} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	plan := func(head, base string) string {
		return fmt.Sprintf(`{"base": %q, "head": %q, "all": false, "reason": "", "suites": [{"suite": "e2e", "all": false, "reason": "", "selected": [{"label": "//e2e:a", "sources": []}], "untouched": [], "whole_suite": []}]}`, base, head)
	}

	forged := t.TempDir()
	writeFile(t, filepath.Join(forged, planOutFile), plan(base, base))

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, forged, os.Stdout); err == nil || handOn {
		t.Fatalf("a plan whose head isn't the change merged into its base: %v %v", handOn, err)
	}

	older := t.TempDir()
	writeFile(t, filepath.Join(older, planOutFile), plan(head, base))

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, older, os.Stdout); err != nil || !handOn {
		t.Fatalf("a plan against the tip before it moved: %v %v", handOn, err)
	}
}
