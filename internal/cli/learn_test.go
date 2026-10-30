package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestLearnStubWarnsWhileWalking(t *testing.T) {
	before := forward.Load()

	t.Cleanup(func() { forward.Store(before) })

	for _, walking := range []bool{false, true} {
		forward.Store(!walking)

		dir := t.TempDir()

		var out bytes.Buffer
		if err := runLearn(t.Context(), []string{"-root", dir, "--stub"}, &out); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(dir, ".pipelinegen", "deps", "REPO.bazel")); err != nil {
			t.Fatalf("walking %v: no stub: %v", walking, err)
		}

		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if lines[0] != "wrote "+filepath.Join(dir, ".pipelinegen", "deps") {
			t.Fatalf("walking %v: %q", walking, out.String())
		}

		warned := len(lines) == 2 && lines[1] == learnStubWarning
		if warned != walking || walking && !strings.Contains(lines[1], "the next release refuses them") {
			t.Fatalf("walking %v: %q", walking, out.String())
		}
	}
}

func TestCheckWarnsOfLeftoverSetupWhileWalking(t *testing.T) {
	before := forward.Load()

	t.Cleanup(func() { forward.Store(before) })

	r := testrepo.New(t)
	r.Write("MODULE.bazel", readFile(t, filepath.Join(r.Dir, "MODULE.bazel"))+"# touched\n")
	r.Git("add", "MODULE.bazel")

	forward.Store(true)

	if out, _, code := run(t, "check", "--root", r.Dir, "--staged"); code != 0 || out != "" {
		t.Fatalf("without the walk: exit %d:\n%s", code, out)
	}

	forward.Store(false)

	out, _, code := run(t, "check", "--root", r.Dir, "--staged")
	if code != 0 || !strings.HasPrefix(out, "warning: MODULE.bazel: declares the pipelinegen_deps repository") || !strings.Contains(out, "\n  fix: remove local_repository") {
		t.Fatalf("walking: exit %d:\n%s", code, out)
	}
}
