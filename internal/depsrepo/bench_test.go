package depsrepo

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func bigInput(units, slices int) (*deps.Tree, []deps.Slice) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // test data
	tree := &deps.Tree{Units: map[string]deps.Unit{}}
	ids := make([]string, 0, units)

	for i := range units {
		group := fmt.Sprintf("internal/pkg%04d", i/10)
		id := fmt.Sprintf("go:%s#func:Func%05d", group, i)
		tree.Units[id] = deps.Unit{ID: id, Group: group, Digest: fmt.Sprintf("%064x", r.Uint64())}
		ids = append(ids, id)
	}

	ss := make([]deps.Slice, 0, slices)

	for i := range slices {
		s := deps.Slice{Suite: "backend", Name: fmt.Sprintf("internal/pkg%04d/x%04d_test.go", i%(units/10), i)}

		for range 200 + r.IntN(400) {
			s.Units = append(s.Units, ids[r.IntN(len(ids))])
		}

		s.Groups = []string{fmt.Sprintf("internal/pkg%04d", r.IntN(units/10))}
		ss = append(ss, s)
	}

	return tree, ss
}

func TestScale(t *testing.T) {
	if os.Getenv("PIPELINEGEN_DEPS_SCALE") == "" {
		t.Skip("measures the writer at scale; set PIPELINEGEN_DEPS_SCALE=1")
	}

	tree, ss := bigInput(20000, 1000)
	d := newDiffer(t)

	// a warm Bazel server, so both hashings pay the same start
	if _, err := testrepo.Bazel(t, d.dir).Run(t.Context(), "query", "//:all"); err != nil {
		t.Fatal(err)
	}

	without := filepath.Join(t.TempDir(), "without.json")
	start := time.Now()

	if err := d.tool.Hashes(t.Context(), testrepo.Bazel(t, d.dir), d.dir, without, without+".edges"); err != nil {
		t.Fatal(err)
	}

	t.Logf("bazel-diff hashes without the repository: %v", time.Since(start))

	start = time.Now()
	files := Files(tree, ss)

	t.Logf("render: %v", time.Since(start))

	start = time.Now()

	if _, err := (Writer{}).Write(t.Context(), d.dir, tree, ss); err != nil {
		t.Fatal(err)
	}

	t.Logf("write: %v", time.Since(start))

	start = time.Now()

	if _, err := (Writer{}).Write(t.Context(), d.dir, tree, ss); err != nil {
		t.Fatal(err)
	}

	t.Logf("write again, unchanged: %v", time.Since(start))

	var total int
	for _, b := range files {
		total += len(b)
	}

	t.Logf("%d files, %.1f MB; slices package BUILD %.1f MB", len(files), float64(total)/1e6, float64(len(files[slicesDir+"/BUILD.bazel"]))/1e6)

	bz := testrepo.Bazel(t, d.dir)
	if _, err := bz.Run(t.Context(), "query", "//:all"); err != nil {
		t.Fatal(err)
	}

	start = time.Now()

	if _, err := bz.Run(t.Context(), "query", "@pipelinegen_deps//slices:all", "--output=label"); err != nil {
		t.Fatal(err)
	}

	t.Logf("bazel loads the slices package: %v", time.Since(start))

	start = time.Now()
	with := filepath.Join(t.TempDir(), "with.json")

	if err := d.tool.Hashes(t.Context(), bz, d.dir, with, with+".edges"); err != nil {
		t.Fatal(err)
	}

	t.Logf("bazel-diff hashes with the repository: %v", time.Since(start))

	id := ss[0].Units[0]
	u := tree.Units[id]
	u.Digest = "changed"
	tree.Units[id] = u

	start = time.Now()

	if _, err := (Writer{}).Write(t.Context(), d.dir, tree, ss); err != nil {
		t.Fatal(err)
	}

	t.Logf("write after one digest changed: %v", time.Since(start))

	head := filepath.Join(t.TempDir(), "head.json")
	start = time.Now()

	if err := d.tool.Hashes(t.Context(), bz, d.dir, head, head+".edges"); err != nil {
		t.Fatal(err)
	}

	t.Logf("bazel-diff hashes after the change: %v", time.Since(start))

	want := 0

	for _, s := range ss {
		if slices.Contains(s.Units, id) {
			want++
		}
	}

	if got := d.selected(with, head); len(got) != want {
		t.Fatalf("selected %d slices, %d list the unit", len(got), want)
	}
}
