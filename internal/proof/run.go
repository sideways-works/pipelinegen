// Package proof plans changes to a small fixture through the released Go plugins after recording its tests,
// against a table of what each should select (docs/proof.md), and again with the forward-binding build.
package proof

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Options struct {
	// Newest plans with each piece's newest release instead of the pins,
	// checked against that release's SHA256SUMS.
	Newest bool
	// Source plans with the tool built from the checkout at Root, so a change to the planning code is
	// proved by its own commit; the plugins stay the pinned releases.
	Source bool
	// Work is the work directory: empty or missing, and everything the
	// run makes stays in it.
	Work string
	// Root is the tool's checkout; its ci/proof/pins are the pins.
	Root string
	// Tool, Plugins (holding go-sbom and go-correlator) and BazelDiff replace the pinned releases;
	// every given binary is checked against its pin.
	Tool, Plugins, BazelDiff string
	// Workers plan the cases side by side (3 when 0), each in its own worktree and warm cache copy with
	// two Bazel servers; Heap bounds each Bazel server's Java heap (1g when empty).
	Workers int
	Heap    string
	Token   string

	Stdout, Stderr io.Writer
}

var ErrUsage = errors.New("usage")

// ErrDiffers is a proof whose plans differ from the table or between the
// two tools.
var ErrDiffers = errors.New("the proof differs")

var errChecksum = errors.New("checksum differs from the pin")

var pieces = []string{"pipelinegen", pieceSBOM, pieceCorrelator}

type pinned struct{ repo, version, sha string }

type run struct {
	o       Options
	work    string
	built   string
	tool    string
	walk    string
	plugins string
	bd      []string
	pins    map[string]pinned
	moved   string
	walking bool
	c0      string
	env     []string

	mu sync.Mutex
}

type planner struct {
	r       *run
	fixture string
	cache   string
	env     []string
	out     io.Writer
}

// Run runs the proof; after printing the table it returns ErrDiffers when a plan differs from the table
// or the two tools plan differently.
func Run(ctx context.Context, o Options) error {
	if o.Newest && o.Source {
		return fmt.Errorf("%w: --newest or --source, not both", ErrUsage)
	}

	if o.Source && o.Tool != "" {
		return fmt.Errorf("%w: --source builds the tool; PIPELINEGEN names another", ErrUsage)
	}

	if err := os.MkdirAll(o.Work, 0o750); err != nil {
		return fmt.Errorf("work: %w", err)
	}

	work, err := filepath.Abs(o.Work)
	if err != nil {
		return fmt.Errorf("work: %w", err)
	}

	if entries, err := os.ReadDir(work); err != nil || len(entries) > 0 {
		return fmt.Errorf("%w: %s is not empty", ErrUsage, work)
	}

	if o.Workers == 0 {
		o.Workers = 3
	}

	if o.Heap == "" {
		o.Heap = "1g"
	}

	r := &run{o: o, work: work}

	return r.run(ctx)
}

func (r *run) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, _ = fmt.Fprintf(r.o.Stderr, "== "+format+"\n", args...) //nolint:errcheck // the log is for people
}

func setEnv(env []string, kv ...string) []string {
	out := slices.Clone(env)

	for _, s := range kv {
		name, _, _ := strings.Cut(s, "=")
		out = slices.DeleteFunc(out, func(e string) bool { return strings.HasPrefix(e, name+"=") })
		out = append(out, s)
	}

	return out
}

func unsetEnv(env []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(e string) bool {
		name, _, _ := strings.Cut(e, "=")

		return slices.Contains(names, name)
	})
}

func (r *run) command(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the proof's own tools and fixture
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr

	return cmd
}

func (r *run) sh(ctx context.Context, dir string, env []string, name string, args ...string) error {
	if err := r.command(ctx, dir, env, r.o.Stdout, r.o.Stderr, name, args...).Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

func (r *run) output(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	var out strings.Builder

	if err := r.command(ctx, dir, env, &out, r.o.Stderr, name, args...).Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return strings.TrimRight(out.String(), "\n"), nil
}

func (r *run) run(ctx context.Context) error {
	work := r.work

	r.env = setEnv(os.Environ(), "GOTOOLCHAIN=local")

	version, err := r.output(ctx, work, r.env, "go", "env", "GOVERSION")
	if err != nil || version != "go1.27.1" {
		return fmt.Errorf("%w: put Go 1.27.1 first on PATH", ErrUsage)
	}

	for _, d := range []string{"bin", "tmp", "gotmp", "cache", "bazel", "remote/example", "record", "plans"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
			return fmt.Errorf("work: %w", err)
		}
	}

	r.env = setEnv(r.env, "TMPDIR="+filepath.Join(work, "tmp"), "GOTMPDIR="+filepath.Join(work, "gotmp"), "GOFLAGS=-trimpath")

	if err := r.buildTools(ctx); err != nil {
		return err
	}

	// the plugins' module cache is the planner's; the tests use the same one
	r.env = setEnv(r.env, "GOMODCACHE="+filepath.Join(work, "cache", "caches", "gomodcache"))

	if r.o.BazelDiff != "" {
		r.bd = []string{flagBazelDiff, r.o.BazelDiff}
	}

	if err := r.releases(ctx); err != nil {
		return err
	}

	if err := r.fetchAll(ctx); err != nil {
		return err
	}

	// The walk reads the links a correlator answers; one that offers none
	// gives it nothing to walk, so the comparison runs only when it does.
	if d, err := r.output(ctx, work, r.env, filepath.Join(r.plugins, pieceCorrelator), "describe"); err == nil && strings.Contains(d, `"links"`) {
		r.walking = true
	}

	if err := r.makeFixture(ctx); err != nil {
		return err
	}

	stub, err := startStub(ctx, filepath.Join(work, "stub.log"))
	if err != nil {
		return err
	}

	// on the way out the stand-in stops, and so do the Bazel servers the
	// run started
	defer r.stop(stub)

	r.env = setEnv(r.env,
		"PIPELINEGEN_GIT_URL=file://"+filepath.Join(work, "remote"),
		"PIPELINEGEN_GITHUB_API=http://"+stub.Addr(),
		"GITHUB_TOKEN=stand-in",
	)

	return r.prove(ctx)
}

func (r *run) buildTools(ctx context.Context) error {
	bin := filepath.Join(r.work, "bin")

	// No VCS stamp: a checkout owned by another user would fail it, and
	// the commit is named in the last line.
	if r.o.Source {
		if err := r.sh(ctx, r.work, r.env, "go", "build", "-C", r.o.Root, "-buildvcs=false", "-o", filepath.Join(bin, "pipelinegen"), "./cmd/pipelinegen"); err != nil {
			return err
		}

		g := []string{"-c", "safe.directory=" + r.o.Root, "-C", r.o.Root}

		var out strings.Builder
		if err := r.command(ctx, r.work, r.env, &out, io.Discard, gitCmd, append(g, "rev-parse", "--short=12", "HEAD")...).Run(); err != nil {
			r.built = "commit unknown"
		} else {
			r.built = strings.TrimRight(out.String(), "\n")
		}

		out.Reset()

		_ = r.command(ctx, r.work, r.env, &out, io.Discard, gitCmd, append(g, "status", "--porcelain")...).Run() //nolint:errcheck // no status is no local edits
		if out.Len() > 0 {
			r.built += " with local edits"
		}
	}

	// The shipped tool walks; only this build tag turns the forward
	// binding back on.
	r.walk = filepath.Join(bin, "pipelinegen-forward")

	return r.sh(ctx, r.work, r.env, "go", "build", "-C", r.o.Root, "-tags", "pipelinegen_forward", "-buildvcs=false", "-o", r.walk, "./cmd/pipelinegen")
}

// readPins reads ci/proof/pins: name, repository, tag, checksum.
func readPins(path string) (map[string]pinned, error) {
	f, err := os.Open(path) //nolint:gosec // the checkout's own pins
	if err != nil {
		return nil, fmt.Errorf("pins: %w", err)
	}

	defer func() { _ = f.Close() }() //nolint:errcheck // read only

	out := map[string]pinned{}
	sc := bufio.NewScanner(f)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}

		if _, seen := out[fields[0]]; !seen {
			out[fields[0]] = pinned{repo: fields[1], version: fields[2], sha: fields[3]}
		}
	}

	return out, nil
}

func (r *run) releases(ctx context.Context) error {
	pins, err := readPins(filepath.Join(r.o.Root, "ci", "proof", "pins"))
	if err != nil {
		return err
	}

	r.pins = map[string]pinned{}

	for _, name := range pieces {
		p := pins[name]

		if r.o.Newest {
			v, s, err := Newest(ctx, r.o.Token, p.repo, name+"-linux-amd64")
			if err != nil {
				return fmt.Errorf("kit: %w", err)
			}

			if v != p.version {
				r.moved += fmt.Sprintf("%s %s to %s, ", name, p.version, v)
			}

			p.version, p.sha = v, s
		}

		r.pins[name] = p
	}

	return nil
}

func (r *run) fetch(ctx context.Context, name, dir, copyFrom string) error {
	p := r.pins[name]

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	out := filepath.Join(dir, name)

	if copyFrom == "" {
		if err := Fetch(ctx, r.o.Token, p.repo, p.version, name+"-linux-amd64", p.sha, out); err != nil {
			return fmt.Errorf("kit: %w", err)
		}

		return nil
	}

	data, err := os.ReadFile(copyFrom) //nolint:gosec // a binary the caller names
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	if err := os.WriteFile(out, data, 0o755); err != nil { //nolint:gosec // an executable
		return fmt.Errorf("fetch: %w", err)
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != p.sha {
		return fmt.Errorf("%s: %w", name, errChecksum)
	}

	if err := os.Chmod(out, 0o755); err != nil { //nolint:gosec // an executable
		return fmt.Errorf("fetch: %w", err)
	}

	return nil
}

func (r *run) fetchAll(ctx context.Context) error {
	bin := filepath.Join(r.work, "bin")
	r.tool = filepath.Join(bin, "pipelinegen")
	r.plugins = filepath.Join(r.work, "tools", "plugins")

	if !r.o.Source {
		if err := r.fetch(ctx, "pipelinegen", bin, r.o.Tool); err != nil {
			return err
		}
	}

	for _, name := range []string{pieceSBOM, pieceCorrelator} {
		from := ""
		if r.o.Plugins != "" {
			from = filepath.Join(r.o.Plugins, name)
		}

		if err := r.fetch(ctx, name, r.plugins, from); err != nil {
			return err
		}
	}

	if r.o.Source {
		r.logf("built: pipelinegen from this commit (%s); checked: go-sbom %s, go-correlator %s", r.built, r.pins[pieceSBOM].version, r.pins[pieceCorrelator].version)
	} else {
		r.logf("checked: pipelinegen %s, go-sbom %s, go-correlator %s", r.pins["pipelinegen"].version, r.pins[pieceSBOM].version, r.pins[pieceCorrelator].version)
	}

	return nil
}

func (r *run) makeFixture(ctx context.Context) error {
	fixture := filepath.Join(r.work, "fixture")

	pins := Pins{
		ToolVersion: r.pins["pipelinegen"].version, ToolSHA: r.pins["pipelinegen"].sha,
		SBOMVersion: r.pins[pieceSBOM].version, SBOMSHA: r.pins[pieceSBOM].sha,
		CorrelatorVersion: r.pins[pieceCorrelator].version, CorrelatorSHA: r.pins[pieceCorrelator].sha,
	}

	if err := WriteFixture(fixture, filepath.Join(r.work, "bazel"), r.o.Heap, pins); err != nil {
		return err
	}

	for _, args := range [][]string{{"go", "mod", "tidy"}, {"go", "mod", "download", expectAll}, {gitCmd, "add", "-A"}, {gitCmd, "commit", "-qm", "the fixture"}} {
		if err := r.sh(ctx, fixture, r.env, args[0], args[1:]...); err != nil {
			return err
		}
	}

	c0, err := r.output(ctx, fixture, r.env, gitCmd, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	r.c0 = c0
	bare := filepath.Join(r.work, "remote", "example", "fixture.git")

	for _, args := range [][]string{
		{gitCmd, "init", "-q", "--bare", "-b", mainBranch, bare},
		{gitCmd, "-C", fixture, "remote", "add", "origin", bare},
		{gitCmd, "-C", fixture, "push", "-q", "origin", mainBranch},
	} {
		if err := r.sh(ctx, r.work, r.env, args[0], args[1:]...); err != nil {
			return err
		}
	}

	r.logf("fixture at %s", c0)

	return nil
}

func (r *run) stop(stub *stub) {
	stub.Close()

	pids, _ := filepath.Glob(filepath.Join(r.work, "bazel", "*", "server", "server.pid.txt")) //nolint:errcheck // a valid pattern

	for _, f := range pids {
		data, err := os.ReadFile(f) //nolint:gosec // Bazel's own pid file
		if err != nil {
			continue
		}

		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Signal(syscall.SIGTERM) //nolint:errcheck // a server gone already is stopped
			}
		}
	}
}
