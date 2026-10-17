package affected

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

type depsDiffer struct {
	fakeDiffer

	targets int
	during  func(dir string)
}

func (d *depsDiffer) Hashes(ctx context.Context, bz *bazel.Client, dir, out, edges string) error {
	if d.during != nil {
		d.during(dir)
	}

	if err := d.fakeDiffer.Hashes(ctx, bz, dir, out+".commit", edges); err != nil {
		return err
	}

	hashes := map[string]string{"//:BUILD.bazel": "h"}
	for i := range d.targets {
		hashes[fmt.Sprintf("@@+_repo_rules+pipelinegen_deps//slices:s%d", i)] = "h"
	}

	data, err := json.Marshal(map[string]any{"hashes": hashes, "metadata": map[string]any{}})
	if err != nil {
		return fmt.Errorf("hashes: %w", err)
	}

	return os.WriteFile(out, data, 0o600) //nolint:wrapcheck // a test's fake
}

const keyFile = ".pipelinegen/test.key"

func checkedOptions(t *testing.T, s *setup, d *depsDiffer, base, head string, warnings *[]string) Options {
	t.Helper()

	return Options{
		Config: s.cfg, Repo: gitx.Repo{Dir: s.r.Dir}, Cache: s.cache, Base: base, Head: head, Differ: d,
		Bazel: func(dir string) *bazel.Client {
			if c, ok := s.bz[dir]; ok {
				return c
			}

			c := testrepo.Bazel(t, dir)
			s.bz[dir] = c

			return c
		},
		Prepare: func(_ context.Context, tree, commit string, _ map[string][]bazel.Slice) (string, error) {
			if err := os.MkdirAll(filepath.Join(tree, ".pipelinegen"), 0o750); err != nil {
				return "", fmt.Errorf("prepare: %w", err)
			}

			return "k-" + commit[:7], os.WriteFile(filepath.Join(tree, keyFile), []byte("k-"+commit[:7]), 0o600)
		},
		Written: func(tree string) (string, error) {
			data, err := os.ReadFile(filepath.Join(tree, keyFile))

			return string(data), err
		},
		Expect: func(string) int { return 2 },
		Warn:   func(format string, args ...any) { *warnings = append(*warnings, fmt.Sprintf(format, args...)) },
	}
}

func TestATreeSwappedMidPlanFailsThePlan(t *testing.T) {
	cases := map[string]func(s *setup, other string) func(dir string){
		"the deps repository rewritten": func(*setup, string) func(dir string) {
			return func(dir string) {
				os.WriteFile(filepath.Join(dir, keyFile), []byte("another plan's"), 0o600) //nolint:errcheck // the test's own file
			}
		},
		"another commit checked out": func(s *setup, other string) func(dir string) {
			return func(dir string) { s.r.GitIn(t, "-C", dir, "checkout", "-q", "--detach", other) }
		},
	}

	for name, swap := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSetup(t)
			other := s.r.Git("rev-parse", "HEAD")
			s.r.Write("docs/w.md", "w\n")
			base := s.r.Commit("w")
			s.r.Write("docs/x.md", "x\n")
			head := s.r.Commit("x")

			var warnings []string

			d := &depsDiffer{fakeDiffer: *s.differ, targets: 10}
			d.during = swap(s, other)

			p, err := Compute(t.Context(), checkedOptions(t, s, d, base, head, &warnings))
			if !errors.Is(err, ErrMoved) {
				t.Fatalf("got %v (%+v), want the tree changed underneath", err, p)
			}

			if left, err := filepath.Glob(filepath.Join(s.cache, "snapshots", "*.json")); err != nil || len(left) > 0 {
				t.Fatalf("a snapshot was kept: %v", left)
			}
		})
	}
}

func TestABadCachedSnapshotIsHashedAgain(t *testing.T) {
	s := newSetup(t)
	base := s.r.Git("rev-parse", "HEAD")
	s.r.Write("docs/x.md", "x\n")
	head := s.r.Commit("x")

	var warnings []string

	d := &depsDiffer{fakeDiffer: *s.differ, targets: 10}

	if _, err := Compute(t.Context(), checkedOptions(t, s, d, base, head, &warnings)); err != nil {
		t.Fatal(err)
	}

	metas, err := filepath.Glob(filepath.Join(s.cache, "snapshots", head+"-*.json"))
	if err != nil || len(metas) != 1 {
		t.Fatalf("snapshots of the head: %v", metas)
	}

	data, err := os.ReadFile(metas[0])
	if err != nil {
		t.Fatal(err)
	}

	data = []byte(strings.Replace(string(data), `"deps_targets":10`, `"deps_targets":1`, 1))
	if err := os.WriteFile(metas[0], data, 0o600); err != nil { //nolint:gosec // the test's own cache
		t.Fatal(err)
	}

	hashed := len(d.hashed)

	if _, err := Compute(t.Context(), checkedOptions(t, s, d, base, head, &warnings)); err != nil {
		t.Fatal(err)
	}

	if len(d.hashed) != hashed+1 || len(warnings) != 1 || !strings.Contains(warnings[0], "hashed again") {
		t.Fatalf("hashed %d more, warnings %v", len(d.hashed)-hashed, warnings)
	}

	d.targets = 1

	s.r.Write("docs/y.md", "y\n")
	next := s.r.Commit("y")

	if _, err := Compute(t.Context(), checkedOptions(t, s, d, head, next, &warnings)); !errors.Is(err, ErrMoved) {
		t.Fatalf("too few targets hashed: %v", err)
	}
}
