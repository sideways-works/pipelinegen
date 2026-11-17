package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// An older pipeline's lock steps run without a config, a token or a run, and must pass.
func TestLockAndUnlockAreHarmless(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, k := range []string{envToken, envRun, envChange, envCommit, envSuite, envBatch, envGitURL} {
		t.Setenv(k, "")
	}

	for _, args := range [][]string{
		{"lock", "acquire", "browsers", "--root", "source", "--started", "begun", "--wait", "1h"},
		{"lock", "acquire", "$PIPELINEGEN_LOCK", "--root", "source"},
		{"lock", "release", "browsers", "--root", "source"},
		{"lock"},
		{"unlock", "browsers", "2", "--repository", "o/r"},
		{"unlock"},
	} {
		stdout, stderr, code := run(t, args...)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "locks are gone") {
			t.Errorf("%v: %d %q %q", args, code, stdout, stderr)
		}
	}

	if readStarted("begun").IsZero() {
		t.Error("lock acquire --started left no stamp for the batch's end")
	}

	for _, name := range []string{"lock", "unlock"} {
		if stdout, _, code := run(t, "help", name); code != 0 || !strings.Contains(stdout, "removed in the next release") {
			t.Errorf("help %s: %d %q", name, code, stdout)
		}
	}

	if !strings.Contains(commandList(), "removed in the next release") {
		t.Error("the command list doesn't say lock is going")
	}
}

func TestThePlanStepSweepsOldLeases(t *testing.T) { sweepsOldLeases(t) }

func TestThePlanStepSweepsOldLeasesOnABranch(t *testing.T) {
	onBranch(t, sweepsOldLeases)
}

func sweepsOldLeases(t *testing.T) {
	t.Helper()

	root, _, s := runEnv(t)

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	lease := []byte(`{"holder":"o/r pr pr-7 0123456789ab e2e","expires":"2999-01-01T00:00:00Z"}` + "\n")

	var b *branchstore.Store
	if onBranchStore {
		b = &branchstore.Store{Remote: s.Remote, Branch: cfg.LocksBranchName(), Roots: true, Dir: filepath.Join(t.TempDir(), "locks")}
		t.Cleanup(func() { b.Close() }) //nolint:errcheck // the test's own scratch

		if err := b.Write(t.Context(), "leases", branchstore.Change{Files: map[string][]byte{
			"locks/browsers/1.json": lease, "locks/continuous-main/1.json": lease, "locks/browsers/wait-0123.json": lease,
		}}); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := s.Write(t.Context(), "leases",
			gitstore.Update{Ref: "refs/pipelinegen/locks/browsers/1", Files: map[string][]byte{"lease.json": lease}},
			gitstore.Update{Ref: "refs/pipelinegen/locks/continuous-main/1", Files: map[string][]byte{"lease.json": lease}},
		); err != nil {
			t.Fatal(err)
		}
	}

	left := func() int {
		t.Helper()

		if b != nil {
			v, err := b.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			files, err := v.Files(t.Context(), "locks")
			if err != nil {
				t.Fatal(err)
			}

			return len(files)
		}

		refs, err := s.List(t.Context(), oldLeasesRefs)
		if err != nil {
			t.Fatal(err)
		}

		return len(refs)
	}

	sweep := func(proposed bool) string {
		t.Helper()

		var stderr bytes.Buffer

		store := openStore(cfg)
		store.Proposed = proposed

		sweepOldLeases(t.Context(), cfg, store, &stderr)

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		return stderr.String()
	}

	if said := sweep(true); said != "" || left() == 0 {
		t.Fatalf("a run that isn't trusted swept: %q, %d left", said, left())
	}

	if said := sweep(false); said != "" || left() != 0 {
		t.Fatalf("swept: %q, %d left", said, left())
	}

	if said := sweep(false); said != "" {
		t.Fatalf("a sweep with nothing to sweep: %q", said)
	}
}
