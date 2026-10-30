package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestAProposedChangeTakesTheTrustedLock(t *testing.T) { proposedTakesTrustedLock(t) }

func TestAProposedChangeTakesTheTrustedLockOnABranch(t *testing.T) {
	onBranch(t, proposedTakesTrustedLock)
}

func proposedTakesTrustedLock(t *testing.T) {
	t.Helper()

	root, _, _ := runEnv(t)

	store := ""
	if onBranchStore {
		store = "store: {backend: branch}\n"
	}

	trustedConfig := strings.Replace(reportConfig, "suites: {e2e: {}}", "suites: {e2e: {lock_minutes: 100}}", 1) + store

	cfg, err := config.Parse([]byte(trustedConfig))
	if err != nil {
		t.Fatal(err)
	}

	blob, err := json.Marshal(cfg.Trusted())
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(envTrusted, string(blob))

	writeFile(t, filepath.Join(root, config.FileName), strings.NewReplacer(
		"lock: {name: browsers, minutes: 90, slots: 1}", "lock: {name: browsers, minutes: 900, slots: 3, per: batch, weight: 3}",
		"suites: {e2e: {}}", "suites: {e2e: {lock_minutes: 1200}}",
	).Replace(reportConfig)+store)

	before := time.Now()

	stdout, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root)
	if code != 0 {
		t.Fatalf("acquire: %d %s %s", code, stdout, stderr)
	}

	until, err := time.Parse(time.RFC3339, strings.TrimSpace(stdout[strings.LastIndex(stdout, " ")+1:]))
	if err != nil {
		t.Fatalf("acquire's line %q: %v", stdout, err)
	}

	// the trusted run's 100 minutes and the margin, not the change's 1200.
	if want := before.Add(115 * time.Minute); until.Before(want.Add(-time.Minute)) || until.After(want.Add(time.Minute)) {
		t.Fatalf("lease until %s, want about %s", until, want)
	}

	l := lockAt(cfg, "browsers", "")

	held, err := l.Leases(t.Context())
	closeLock(l, &err)

	if err != nil || len(held) != 1 {
		t.Fatalf("the trusted weight takes one slot: %v (%v)", held, err)
	}

	// the trusted lock has one slot, not the change's three.
	t.Setenv(envBatch, "2")

	if _, stderr, code := run(t, "lock", "acquire", "browsers", "-wait", "100ms", "-root", root); code != 1 || !strings.Contains(stderr, "batch 1") {
		t.Fatalf("a second holder of a one-slot lock: %d %s", code, stderr)
	}
}
