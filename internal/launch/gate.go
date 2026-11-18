package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var errVersions = errors.New("versions")

// Lint checks that .tool-versions names the Go and golangci-lint that Bazel pins, runs
// golangci-lint over every package with a cache of its own, so no result of a run with another
// version or config is replayed, and then pipelinegen's own check of the checkout.
func (l *Launch) Lint(ctx context.Context) error {
	if err := l.versions(ctx); err != nil {
		return err
	}

	cache, err := os.MkdirTemp("", "pipelinegen-lint-cache-")
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	defer func() { _ = os.RemoveAll(cache) }() //nolint:errcheck // a scratch directory

	env := append(l.env(), "GOLANGCI_LINT_CACHE="+cache)
	if err := l.command(ctx, l.workspace, env, l.lint, "run", "./...").Run(); err != nil {
		return fmt.Errorf("lint: golangci-lint: %w", err)
	}

	code, err := l.run(ctx, l.workspace, "./cmd/pipelinegen", []string{"check"})
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("lint: pipelinegen check exited %d", code) //nolint:err113 // its findings are above
	}

	return nil
}

func (l *Launch) versions(ctx context.Context) error {
	data, err := os.ReadFile(filepath.Join(l.workspace, ".tool-versions"))
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	goVersion, err := l.output(ctx, l.goTool, "env", "GOVERSION")
	if err != nil {
		return err
	}

	lintVersion, err := l.output(ctx, l.lint, "version", "--short")
	if err != nil {
		return err
	}

	return CheckVersions(string(data), map[string]string{
		"golang":        strings.TrimPrefix(goVersion, "go"),
		"golangci-lint": strings.TrimPrefix(lintVersion, "v"),
	})
}

func (l *Launch) output(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer

	cmd := l.command(ctx, l.workspace, l.env(), name, args...)
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("launch: %s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}

	return strings.TrimSpace(out.String()), nil
}

// CheckVersions fails unless toolVersions (asdf's .tool-versions) names each tool at the version
// Bazel pins for it.
func CheckVersions(toolVersions string, pinned map[string]string) error {
	named := map[string]string{}

	for line := range strings.SplitSeq(toolVersions, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && !strings.HasPrefix(f[0], "#") {
			named[f[0]] = f[1]
		}
	}

	var errs []error

	for _, tool := range sortedKeys(pinned) {
		if named[tool] != pinned[tool] {
			errs = append(errs, fmt.Errorf("%w: .tool-versions names %s %q, MODULE.bazel pins %s", errVersions, tool, named[tool], pinned[tool]))
		}
	}

	return errors.Join(errs...)
}

// Gate is what a branch passes before it lands: build, vet and lint, then every test of pkgs and
// the proof with the tool built from the checkout, in work (a temporary directory when empty).
func (l *Launch) Gate(ctx context.Context, work string, pkgs []string) error {
	for _, s := range []struct {
		name string
		run  func() error
	}{
		{"build", func() error { return l.goCmd(ctx, l.Stdout, "build", "./...") }},
		{"vet", func() error { return l.goCmd(ctx, l.Stdout, "vet", "./...") }},
		{"lint", func() error { return l.Lint(ctx) }},
	} {
		if err := l.stage(s.name, s.run); err != nil {
			return err
		}
	}

	tally := &Tally{Out: l.Stdout}
	errTests := l.stage("tests", func() error {
		return l.goCmd(ctx, tally, append([]string{"test", "-timeout", "60m"}, pkgs...)...)
	})

	sayf(l.Stdout, "== packages ok: %d, failed: %d\n", tally.OK, tally.Failed)

	for _, line := range tally.Failures {
		sayf(l.Stdout, "%s\n", line)
	}

	return errors.Join(errTests, l.stage("proof", func() error { return l.proof(ctx, work) }))
}

func (l *Launch) stage(name string, run func() error) error {
	start := time.Now()
	err := run()
	took := time.Since(start).Round(time.Second)

	if err != nil {
		sayf(l.Stdout, "== %s FAILED after %s\n", name, took)

		return fmt.Errorf("gate: %s: %w", name, err)
	}

	sayf(l.Stdout, "== %s ok (%s)\n", name, took)

	return nil
}

func (l *Launch) goCmd(ctx context.Context, stdout io.Writer, args ...string) error {
	cmd := l.command(ctx, l.workspace, l.env(), l.goTool, args...)
	cmd.Stdout = stdout

	return cmd.Run() //nolint:wrapcheck // the stage names it
}

func (l *Launch) proof(ctx context.Context, work string) error {
	keep := work != ""

	if !keep {
		var err error
		if work, err = os.MkdirTemp("", "pipelinegen-gate-proof-"); err != nil {
			return fmt.Errorf("proof: %w", err)
		}
	}

	code, err := l.run(ctx, l.workspace, "./ci/proof", []string{"--source", work})
	if err == nil && code != 0 {
		err = fmt.Errorf("exited %d; its work directory %s stays", code, work) //nolint:err113 // its output is above
	}

	if err == nil && !keep {
		_ = os.RemoveAll(work) //nolint:errcheck // a scratch directory
	}

	return err
}

var failure = regexp.MustCompile(`^(FAIL|--- *FAIL|panic:)`)

// Tally passes go test's output on and counts its packages that passed and failed, keeping the
// first lines that name a failure.
type Tally struct {
	Out        io.Writer
	OK, Failed int
	Failures   []string

	partial []byte
}

func (t *Tally) Write(p []byte) (int, error) {
	t.partial = append(t.partial, p...)

	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}

		line := string(t.partial[:i])
		t.partial = t.partial[i+1:]

		switch {
		case strings.HasPrefix(line, "ok  "):
			t.OK++
		case strings.HasPrefix(line, "FAIL\t"):
			t.Failed++
		}

		if failure.MatchString(line) && len(t.Failures) < 40 {
			t.Failures = append(t.Failures, line)
		}
	}

	return t.Out.Write(p) //nolint:wrapcheck // a writer passing on
}
