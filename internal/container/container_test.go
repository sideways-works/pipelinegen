package container

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTheCommandLineMountsPassesTheEnvironmentAndPullsByDigest(t *testing.T) {
	r := Run{
		Tool: "docker", Image: "busybox@sha256:" + strings.Repeat("ab", 32), Name: "pipelinegen-unit-batch-1", Dir: "/w/unit/build",
		Mounts: []Mount{{Host: "/w/unit", At: "/w/unit"}, {Host: "/repo/.git", At: "/repo/.git", ReadOnly: true}},
		Env:    map[string]string{"PIPELINEGEN_SUITE": "unit", "B": "2", "A": "1"},
		User:   "1000:1000", Privileged: true,
		Cmd: []string{"/w/unit/build/tools/pipelinegen", "slice", "run"},
	}

	got, err := r.Args()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"run", "--rm", "--name", "pipelinegen-unit-batch-1", "--network", "bridge", "--workdir", "/w/unit/build",
		"--privileged", "--user", "1000:1000",
		"--volume", "/w/unit:/w/unit", "--volume", "/repo/.git:/repo/.git:ro",
		"--env", "A=1", "--env", "B=2", "--env", "PIPELINEGEN_SUITE=unit",
		"busybox@sha256:" + strings.Repeat("ab", 32),
		"/w/unit/build/tools/pipelinegen", "slice", "run",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("args:\n got %q\nwant %q", got, want)
	}
}

// A step that runs before the suite's (installing what it declares, later) runs in the same
// container, by a shell, ahead of the command, and what runs after it whatever its status.
func TestBeforeAndAfterRunInTheSameContainer(t *testing.T) {
	r := Run{Tool: "podman", Image: "alpine:3", Name: "n", Dir: "/", Before: []string{"apk add git"}, After: []string{"echo done"}, Cmd: []string{"make", "test"}}

	got, err := r.Args()
	if err != nil {
		t.Fatal(err)
	}

	i := slices.Index(got, "alpine:3")
	if i < 0 || !slices.Equal(got[i+1:i+3], []string{"sh", "-c"}) || !slices.Equal(got[i+4:], []string{"sh", "make", "test"}) {
		t.Fatalf("args: %q", got)
	}

	if script := got[i+3]; !strings.Contains(script, "set -e\napk add git\nset +e\n\"$@\"\nrc=$?\necho done\nexit $rc") {
		t.Fatalf("script: %q", script)
	}
}

func TestAPathWithAColonIsRefused(t *testing.T) {
	r := Run{Tool: "docker", Image: "busybox", Name: "n", Dir: "/", Mounts: []Mount{{Host: "/a:b", At: "/a:b"}}}

	if _, err := r.Args(); err == nil {
		t.Fatal("no error")
	}
}

func TestFindTakesTheNamedTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if _, err := Find(""); !errors.Is(err, ErrNoTool) {
		t.Fatalf("no tool: %v", err)
	}

	if _, err := Find("podman"); err == nil {
		t.Fatal("a tool that isn't on PATH")
	}
}

// One container from a light image, when docker or podman runs here: the mounted directory is
// written as this machine's user, and only the environment given reaches it.
func TestATinySuiteRunsInBusybox(t *testing.T) {
	tool, err := Find("")
	if err != nil || !Reachable(t.Context(), tool) {
		t.Skip("no container tool here, or it isn't running")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	var log bytes.Buffer
	if _, err := Ensure(ctx, tool, "busybox:1.37.0", &log); err != nil {
		t.Skipf("busybox can't be pulled here: %v\n%s", err, log.String())
	}

	dir := t.TempDir()
	user, userns, handBack := Owner(ctx, tool, false)

	r := Run{
		Tool: tool, Image: "busybox:1.37.0", Name: Name("test"), Dir: dir,
		Mounts: []Mount{{Host: dir, At: dir}}, Env: map[string]string{"GREETING": "hello"}, User: user, UserNS: userns,
		Cmd: []string{"sh", "-c", `echo "$GREETING ${SECRET_TOKEN:-unset}" > out.txt`},
	}

	if handBack {
		r.After = HandBack(dir)
	}

	t.Setenv("SECRET_TOKEN", "never-passed")

	cmd, err := r.Command(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(data) != "hello unset\n" {
		t.Fatalf("out.txt: %q %v", data, err)
	}

	// the test's directory goes after: the file must be this user's to remove
	if err := os.Remove(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal(err)
	}

	if err := exec.CommandContext(ctx, tool, "inspect", r.Name).Run(); err == nil {
		t.Fatalf("the container %s is still there", r.Name)
	}
}
