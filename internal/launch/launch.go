// Package launch is what this repository's `bazel run` targets run: the lint, the gate and the
// programs under ci/, with the Go SDK and golangci-lint that MODULE.bazel pins, in the checkout.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const usage = "usage: launch --go=RLOCATION [--lint=RLOCATION] [--path=RLOCATION]... lint | gate [--proof-work DIR] [PACKAGE...] | run PACKAGE [ARG...]"

var errUsage = errors.New(usage)

// Launch is one run of a target: the environment `bazel run` gave it and where it writes.
type Launch struct {
	Env            []string
	Executable     string
	Stdout, Stderr io.Writer

	workspace, working string
	goTool, lint       string
	// paths are directories that come first on PATH after the Go SDK's, each a tool's (gh).
	paths []string
}

// Main runs args (the target's own arguments, then the ones given after `bazel run ... --`) and
// returns the exit code.
func (l *Launch) Main(ctx context.Context, args []string) int {
	code, err := l.main(ctx, args)
	if err != nil {
		sayf(l.Stderr, "%v\n", err)

		return max(code, 1)
	}

	return code
}

func (l *Launch) main(ctx context.Context, args []string) (int, error) {
	var (
		goPath, lintPath string
		tools            []string
	)

	for len(args) > 0 {
		if v, ok := strings.CutPrefix(args[0], "--go="); ok {
			goPath = v
		} else if v, ok := strings.CutPrefix(args[0], "--lint="); ok {
			lintPath = v
		} else if v, ok := strings.CutPrefix(args[0], "--path="); ok {
			tools = append(tools, v)
		} else {
			break
		}

		args = args[1:]
	}

	if goPath == "" || len(args) == 0 {
		return 2, errUsage
	}

	if err := l.setUp(goPath, lintPath, tools); err != nil {
		return 2, err
	}

	switch args[0] {
	case "lint":
		if len(args) != 1 || l.lint == "" {
			return 2, errUsage
		}

		return 0, l.Lint(ctx)
	case "gate":
		if l.lint == "" {
			return 2, errUsage
		}

		work, pkgs, err := gateArgs(args[1:])
		if err != nil {
			return 2, err
		}

		return 0, l.Gate(ctx, work, pkgs)
	case "run":
		if len(args) < 2 {
			return 2, errUsage
		}

		return l.Run(ctx, args[1], args[2:])
	}

	return 2, errUsage
}

func (l *Launch) setUp(goPath, lintPath string, tools []string) error {
	l.workspace = getenv(l.Env, "BUILD_WORKSPACE_DIRECTORY")
	if l.workspace == "" {
		return errors.New("launch: run it with bazel run, which names the checkout in BUILD_WORKSPACE_DIRECTORY") //nolint:err113 // said once, to the person
	}

	l.working = cmpOr(getenv(l.Env, "BUILD_WORKING_DIRECTORY"), l.workspace)

	var err error

	if l.goTool, err = Rlocation(l.Env, l.Executable, goPath); err != nil {
		return err
	}

	if lintPath != "" {
		if l.lint, err = Rlocation(l.Env, l.Executable, lintPath); err != nil {
			return err
		}
	}

	for _, t := range tools {
		p, err := Rlocation(l.Env, l.Executable, t)
		if err != nil {
			return err
		}

		l.paths = append(l.paths, filepath.Dir(p))
	}

	return nil
}

func sayf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // the log is for people
}

func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func gateArgs(args []string) (string, []string, error) {
	var work string

	for len(args) > 0 {
		switch {
		case args[0] == "--proof-work" && len(args) > 1:
			work, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--proof-work="):
			work, args = strings.TrimPrefix(args[0], "--proof-work="), args[1:]
		case strings.HasPrefix(args[0], "-"):
			return "", nil, errUsage
		default:
			return work, args, nil
		}
	}

	return work, []string{"./..."}, nil
}

// Rlocation is the file a target's runfiles hold at path (an $(rlocationpath) value), with its
// symbolic links resolved: a Go SDK finds its root from where its go binary really is.
func Rlocation(env []string, executable, path string) (string, error) {
	if manifest := getenv(env, "RUNFILES_MANIFEST_FILE"); manifest != "" && getenv(env, "RUNFILES_DIR") == "" {
		return fromManifest(manifest, path)
	}

	dir := cmpOr(getenv(env, "RUNFILES_DIR"), executable+".runfiles")

	p, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("launch: %s is not in the runfiles: %w", path, err)
	}

	return p, nil
}

func fromManifest(manifest, path string) (string, error) {
	data, err := os.ReadFile(manifest) //nolint:gosec // the manifest Bazel names
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if from, to, ok := strings.Cut(line, " "); ok && from == path {
			return filepath.EvalSymlinks(to) //nolint:wrapcheck // the path says it
		}
	}

	return "", fmt.Errorf("launch: %s is not in the runfiles manifest %s", path, manifest) //nolint:err113 // said once, to the person
}

func getenv(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}

	return ""
}

// bazelRun are the variables `bazel run` sets for the target alone: a test or a program started
// from it that runs Bazel or reads runfiles of its own must not see them.
var bazelRun = []string{"BUILD_WORKSPACE_DIRECTORY", "BUILD_WORKING_DIRECTORY", "BUILD_EXECROOT", "BUILD_ID", "JAVA_RUNFILES", "PYTHON_RUNFILES"}

// fromBazelRun says whether a variable is one Bazel sets for what it runs, so a child must not see
// it. TEST_TMPDIR stays: Bazel sets it only for a test, never for `bazel run`, and the tests read the
// person's own as where their Bazel keeps its output.
func fromBazelRun(name string) bool {
	return slices.Contains(bazelRun, name) || strings.HasPrefix(name, "RUNFILES_") ||
		(strings.HasPrefix(name, "TEST_") && name != "TEST_TMPDIR")
}

// ChildEnv is env for what a target starts, as `go run` would start it: PATH as it is with the
// pinned Go first, then the directories of the other pinned tools; Go used as it is (no GOROOT of
// another SDK, no switch to another toolchain); and nothing `bazel run` set.
func ChildEnv(env []string, goTool string, tools ...string) []string {
	out := make([]string, 0, len(env)+2)

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if fromBazelRun(name) || name == "GOROOT" || name == "GOTOOLCHAIN" || name == "PATH" {
			continue
		}

		out = append(out, kv)
	}

	path := strings.Join(append([]string{filepath.Dir(goTool)}, tools...), string(filepath.ListSeparator))
	if p := getenv(env, "PATH"); p != "" {
		path += string(filepath.ListSeparator) + p
	}

	return append(out, "PATH="+path, "GOTOOLCHAIN=local")
}

func (l *Launch) command(ctx context.Context, dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the repository's own tools and programs
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = l.Stdout, l.Stderr
	// a stopped target stops what it started, and gives it time to stop its own servers
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second

	return cmd
}

func (l *Launch) env() []string { return ChildEnv(l.Env, l.goTool, l.paths...) }

// build builds the main package pkg of the checkout into a temporary directory, which the caller
// removes.
func (l *Launch) build(ctx context.Context, pkg string) (string, error) {
	dir, err := os.MkdirTemp("", "pipelinegen-launch-")
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}

	exe := filepath.Join(dir, filepath.Base(pkg))

	// no VCS stamp: a checkout of another user (a CI input) is one git won't read
	cmd := l.command(ctx, l.workspace, l.env(), l.goTool, "build", "-buildvcs=false", "-o", exe, pkg)
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(dir) //nolint:errcheck // a scratch directory

		return "", fmt.Errorf("launch: go build %s: %w", pkg, err)
	}

	return exe, nil
}

// Run builds the checkout's program pkg (./ci/release) and runs it with args where `bazel run`
// was started, as `go run` would; its exit code is the target's.
func (l *Launch) Run(ctx context.Context, pkg string, args []string) (int, error) {
	return l.run(ctx, l.working, pkg, args)
}

func (l *Launch) run(ctx context.Context, dir, pkg string, args []string) (int, error) {
	exe, err := l.build(ctx, pkg)
	if err != nil {
		return 1, err
	}

	defer func() { _ = os.RemoveAll(filepath.Dir(exe)) }() //nolint:errcheck // a scratch directory

	err = l.command(ctx, dir, l.env(), exe, args...).Run()

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}

		return exit.ExitCode(), nil
	}

	if err != nil {
		return 1, fmt.Errorf("launch: %s: %w", pkg, err)
	}

	return 0, nil
}
