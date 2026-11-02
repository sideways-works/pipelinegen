package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runExe(t *testing.T, exe, dir string, env map[string]string, args ...string) (string, string, int) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), exe, args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), envTestCommand+"=1")

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}

		return stdout.String(), stderr.String(), exit.ExitCode()
	}

	return stdout.String(), stderr.String(), 0
}

func copyExe(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}

	defer in.Close() //nolint:errcheck // read only

	to := filepath.Join(t.TempDir(), "pipelinegen")

	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY, 0o700) //nolint:gosec // the test's own copy
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}

	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	return to
}

func readLink(t *testing.T, p string) string {
	t.Helper()

	got, err := os.Readlink(p)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestASecondRecordReusesItsWorkDirectory(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)
	work := filepath.Join(t.TempDir(), "w")
	build := filepath.Join(work, "build")

	if stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root, "--work", work); code != 0 {
		t.Fatalf("the first record: %d %s\n%s", code, stdout, stderr)
	}

	first, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if got := readLink(t, filepath.Join(build, "tools", "pipelinegen")); got != first {
		t.Fatalf("tools/pipelinegen after the first: %s", got)
	}

	if stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root, "--work", work); code != 0 {
		t.Fatalf("the same record again: %d %s\n%s", code, stdout, stderr)
	}

	stale := []string{
		"coverage/_setup/fake/touched.txt", "coverage/e2e/pay.spec.ts/stale/touched.txt", "result/.plan-checked",
		"result/stale.json", "batches/9/slices.json", "work/stub/stale", "prepared/stale", "tools/stale",
	}
	for _, p := range stale {
		writeFile(t, filepath.Join(build, p), "unit:stale\n")
	}

	writeFile(t, filepath.Join(build, "cache", "kept"), "kept\n")

	if err := os.Remove(filepath.Join(build, "tools", "bazelisk")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	if err := os.Symlink("/nowhere/bazelisk", filepath.Join(build, "tools", "bazelisk")); err != nil {
		t.Fatal(err)
	}

	gitT(t, f.root, "worktree", "add", "--quiet", "--detach", filepath.Join(build, "source"), "HEAD")

	second := copyExe(t)

	stdout, stderr, code := runExe(t, second, f.root, f.env, "record", "--suite", "e2e", "--root", f.root, "--work", work)
	if code != 0 {
		t.Fatalf("the second record: %d %s\n%s", code, stdout, stderr)
	}

	if !strings.Contains(stderr, "pipelinegen record: removed what an earlier recording left in "+build+": ") {
		t.Errorf("no line of what it removed:\n%s", stderr)
	}

	for _, p := range stale {
		if data, err := os.ReadFile(filepath.Join(build, p)); err == nil && string(data) == "unit:stale\n" {
			t.Errorf("%s outlived the recording that left it", p)
		}
	}

	if _, err := os.Stat(filepath.Join(build, "cache", "kept")); err != nil {
		t.Errorf("the cache went: %v", err)
	}

	for slice, list := range recordedLists(t, f.root) {
		if strings.Contains(list, "unit:stale") {
			t.Errorf("%s is bound to what an earlier recording left: %s", slice, list)
		}
	}

	if got := readLink(t, filepath.Join(build, "tools", "pipelinegen")); got != second {
		t.Fatalf("tools/pipelinegen after the second: %s, want %s", got, second)
	}

	if got, err := os.Readlink(filepath.Join(build, "tools", "bazelisk")); err == nil && got == "/nowhere/bazelisk" {
		t.Fatal("the stale bazelisk link stayed")
	}
}
