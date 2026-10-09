package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func seeded(t *testing.T, remote, seed string) *Store {
	t.Helper()

	if onBranch {
		t.Skip("a seed is the refs store's scratch repository")
	}

	return &Store{Remote: remote, Seed: seed}
}

func big(t *testing.T, remote, prefix string, n int) {
	t.Helper()

	ups := make([]Update, 0, n)
	for i := range n {
		ups = append(ups, Update{Ref: fmt.Sprintf("%s%03d", prefix, i), Files: files("data", strings.Repeat(fmt.Sprintf("%s %d ", prefix, i), 4000))})
	}

	if _, err := newStore(t, remote).Write(t.Context(), "big", ups...); err != nil {
		t.Fatal(err)
	}
}

func objects(t *testing.T, dir string) int64 {
	t.Helper()

	var total int64

	err := filepath.Walk(filepath.Join(dir, "objects"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return total
}

func list(t *testing.T, s *Store) (int, int64) {
	t.Helper()

	got, err := s.List(t.Context(), "refs/pipelinegen/runs/")
	if err != nil {
		t.Fatal(err)
	}

	before := objects(t, s.Dir)

	for _, ref := range got {
		if _, err := s.Files(t.Context(), ref.Commit); err != nil {
			t.Fatal(err)
		}
	}

	if objects(t, s.Dir) != before {
		t.Error("reading a listed record fetched again")
	}

	return len(got), before
}

func TestTheNextJobFetchesOnlyWhatIsNew(t *testing.T) {
	remote := bare(t)
	seed := filepath.Join(t.TempDir(), "cache", "store", "repo")

	big(t, remote, "refs/pipelinegen/runs/a/", 30)

	first := seeded(t, remote, seed)

	n, cold := list(t, first)
	if n != 30 {
		t.Fatalf("listed %d", n)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(seed, "HEAD")); err != nil {
		t.Fatalf("the seed was not put back: %v", err)
	}

	big(t, remote, "refs/pipelinegen/runs/b/", 1)

	second := seeded(t, remote, seed)

	n, warm := list(t, second)
	if n != 31 {
		t.Fatalf("listed %d", n)
	}

	if grown := warm - cold; grown <= 0 || grown > cold/10 {
		t.Errorf("the second job's repository grew %d bytes over the first's %d; want one record's worth", grown, cold)
	}

	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJobsAtOnceEachHaveTheirOwn(t *testing.T) {
	remote := bare(t)
	seed := filepath.Join(t.TempDir(), "store", "repo")

	big(t, remote, "refs/pipelinegen/runs/a/", 5)

	warm := seeded(t, remote, seed)
	list(t, warm)

	if err := warm.Close(); err != nil {
		t.Fatal(err)
	}

	const jobs = 6

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		dirs = map[string]bool{}
	)

	for range jobs {
		wg.Go(func() {
			s := seeded(t, remote, seed)

			got, err := s.List(t.Context(), "refs/pipelinegen/runs/")
			if err != nil || len(got) != 5 {
				t.Errorf("listed %d: %v", len(got), err)
			}

			mu.Lock()
			if dirs[s.Dir] {
				t.Errorf("two jobs in %s", s.Dir)
			}

			dirs[s.Dir] = true
			mu.Unlock()

			time.Sleep(50 * time.Millisecond)

			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	left, err := os.ReadDir(filepath.Dir(seed))
	if err != nil {
		t.Fatal(err)
	}

	if len(left) != 1 || left[0].Name() != "repo" {
		t.Errorf("left behind %v; want the seed alone", left)
	}
}

func TestABrokenSeedIsRebuilt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(t *testing.T, seed string)
	}{
		{"pack truncated", func(t *testing.T, seed string) {
			t.Helper()

			packs, err := filepath.Glob(filepath.Join(seed, "objects", "pack", "*.pack"))
			if err != nil || len(packs) == 0 {
				t.Fatalf("no pack in the seed: %v", err)
			}

			for _, p := range packs {
				if err := os.Chmod(p, 0o600); err != nil {
					t.Fatal(err)
				}

				if err := os.Truncate(p, 100); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"half written", func(t *testing.T, seed string) {
			t.Helper()

			if err := os.RemoveAll(filepath.Join(seed, "objects")); err != nil {
				t.Fatal(err)
			}
		}},
		{"not a repository", func(t *testing.T, seed string) {
			t.Helper()

			if err := os.RemoveAll(seed); err != nil {
				t.Fatal(err)
			}

			if err := os.MkdirAll(seed, 0o750); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(seed, "junk"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := bare(t)
			seed := filepath.Join(t.TempDir(), "store", "repo")

			big(t, remote, "refs/pipelinegen/runs/a/", 5)

			first := seeded(t, remote, seed)
			list(t, first)

			if err := first.Close(); err != nil {
				t.Fatal(err)
			}

			if _, err := newStore(t, remote).Write(t.Context(), "gc", Update{Ref: "refs/pipelinegen/runs/a/000", Any: true, Files: files("data", "changed")}); err != nil {
				t.Fatal(err)
			}

			repack(t, seed)
			tc.breakIt(t, seed)

			s := seeded(t, remote, seed)

			got, err := s.List(t.Context(), "refs/pipelinegen/runs/")
			if err != nil || len(got) != 5 {
				t.Fatalf("listed %d: %v", len(got), err)
			}

			data, ok, err := s.File(t.Context(), got["refs/pipelinegen/runs/a/000"].Commit, "data")
			if err != nil || !ok || string(data) != "changed" {
				t.Errorf("read %q %v %v", data, ok, err)
			}

			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func repack(t *testing.T, dir string) {
	t.Helper()

	s := &Store{Dir: dir}
	if _, err := s.git(context.Background(), nil, nil, "repack", "-a", "-d", "-q"); err != nil {
		t.Fatal(err)
	}
}

func TestADeadJobsRepositoryIsRemoved(t *testing.T) {
	remote := bare(t)
	seed := filepath.Join(t.TempDir(), "store", "repo")
	dead := seed + seedJob + "123"

	if err := os.MkdirAll(dead, 0o750); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * orphanAge)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}

	s := seeded(t, remote, seed)
	if _, err := s.List(t.Context(), "refs/pipelinegen/runs/"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the dead job's repository stays: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
