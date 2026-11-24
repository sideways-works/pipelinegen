package storetest

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Backend makes a new, empty backend, and opens stores of it: two stores of one backend are two
// processes of one repository or bucket.
type Backend func(t *testing.T) (open func(t *testing.T) *gitstore.Store)

const conformPrefix = "refs/pipelinegen/conform/"

// Conform tests the promises the core relies on of a store, whatever keeps it. A store reads only
// versions it listed or found itself, as the core does: a write that
// answered is seen by every later read, a guarded write is a compare-and-swap on its record, many
// writers of different records lose none of them, one creator of a record wins, a version's files
// never change, and a listing names every record under its prefixes.
func Conform(t *testing.T, backend Backend) {
	t.Helper()

	for name, test := range map[string]func(*testing.T, func(*testing.T) *gitstore.Store){
		"write, list and read":        conformWriteListRead,
		"a guard is compare-and-swap": conformGuards,
		"first is in order":           conformFirst,
		"files by path and dir":       conformFiles,
		"one creator wins":            conformRace,
		"many writers lose nothing":   conformManyWriters,
		"a version never changes":     conformVersions,
	} {
		t.Run(name, func(t *testing.T) { test(t, backend(t)) })
	}
}

func files(kv ...string) map[string][]byte {
	out := map[string][]byte{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = []byte(kv[i+1])
	}

	return out
}

func conformWriteListRead(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()
	a, b := open(t), open(t)

	if refs, err := a.List(ctx, conformPrefix); err != nil || len(refs) != 0 {
		t.Fatalf("empty: %v %v", refs, err)
	}

	written, err := a.Write(ctx, "two", gitstore.Update{Ref: conformPrefix + "x/one", Files: files("state.json", "1", "dir/rec.txt", "r")},
		gitstore.Update{Ref: conformPrefix + "x/two", Files: files("state.json", "2")})
	if err != nil || len(written) != 2 {
		t.Fatalf("write: %v %v", written, err)
	}

	refs, err := b.List(ctx, conformPrefix+"x/", "refs/pipelinegen/other/")
	if err != nil || len(refs) != 2 || refs[conformPrefix+"x/one"].Commit != written[conformPrefix+"x/one"] {
		t.Fatalf("list: %v %v", refs, err)
	}

	if age := time.Since(refs[conformPrefix+"x/one"].Time); age < -time.Minute || age > time.Minute {
		t.Fatalf("time %v", refs[conformPrefix+"x/one"].Time)
	}

	got, err := b.Files(ctx, refs[conformPrefix+"x/one"].Commit)
	if err != nil || string(got["state.json"]) != "1" || string(got["dir/rec.txt"]) != "r" || len(got) != 2 {
		t.Fatalf("files: %q %v", got, err)
	}

	if _, err := a.Write(ctx, "rm", gitstore.Update{Ref: conformPrefix + "x/two", Old: written[conformPrefix+"x/two"], Delete: true}); err != nil {
		t.Fatal(err)
	}

	if refs, err := b.List(ctx, conformPrefix+"x/"); err != nil || len(refs) != 1 {
		t.Fatalf("after delete: %v %v", refs, err)
	}
}

func conformGuards(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()
	s := open(t)
	ref := conformPrefix + "guarded"

	first, err := s.Write(ctx, "create", gitstore.Update{Ref: ref, Files: files("v", "1")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "create again", gitstore.Update{Ref: ref, Files: files("v", "x")}); !errors.Is(err, gitstore.ErrStale) {
		t.Fatalf("a second create: %v", err)
	}

	second, err := s.Write(ctx, "swap", gitstore.Update{Ref: ref, Old: first[ref], Files: files("v", "2")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "stale swap", gitstore.Update{Ref: ref, Old: first[ref], Files: files("v", "x")}); !errors.Is(err, gitstore.ErrStale) {
		t.Fatalf("a swap from an old version: %v", err)
	}

	if _, err := s.Write(ctx, "stale delete", gitstore.Update{Ref: ref, Old: first[ref], Delete: true}); !errors.Is(err, gitstore.ErrStale) {
		t.Fatalf("a delete from an old version: %v", err)
	}

	if _, err := s.Write(ctx, "any", gitstore.Update{Ref: ref, Any: true, Files: files("v", "3")}); err != nil {
		t.Fatalf("a write whatever the record holds: %v", err)
	}

	r := open(t)

	got, ok, err := r.First(ctx, []string{ref})
	if err != nil || !ok || got.Commit == second[ref] {
		t.Fatalf("after any: %+v %v %v", got, ok, err)
	}

	data, ok, err := r.File(ctx, got.Commit, "v")
	if err != nil || !ok || string(data) != "3" {
		t.Fatalf("its file: %q %v %v", data, ok, err)
	}
}

func conformFirst(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()
	s := open(t)

	if _, err := s.Write(ctx, "b and c", gitstore.Update{Ref: conformPrefix + "b", Files: files("n", "b")},
		gitstore.Update{Ref: conformPrefix + "c", Files: files("n", "c")}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := open(t).First(ctx, []string{conformPrefix + "a", conformPrefix + "c", conformPrefix + "b"})
	if err != nil || !ok || got.Name != conformPrefix+"c" {
		t.Fatalf("first: %+v %v %v", got, ok, err)
	}

	if _, ok, err := s.First(ctx, []string{conformPrefix + "none"}); err != nil || ok {
		t.Fatalf("none: %v %v", ok, err)
	}
}

func conformFiles(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()
	s := open(t)

	written, err := s.Write(ctx, "files", gitstore.Update{Ref: conformPrefix + "f", Files: files(
		"state.json", "s", "recording.txt", "r", "files/a/shot.png", "png", "files/b.txt", "b")})
	if err != nil {
		t.Fatal(err)
	}

	other, err := s.Write(ctx, "other", gitstore.Update{Ref: conformPrefix + "g", Files: files("state.json", "t")})
	if err != nil {
		t.Fatal(err)
	}

	r := open(t)

	refs, err := r.List(ctx, conformPrefix)
	if err != nil || refs[conformPrefix+"f"].Commit != written[conformPrefix+"f"] {
		t.Fatalf("list: %v %v", refs, err)
	}

	v := refs[conformPrefix+"f"].Commit

	if got, err := r.Under(ctx, v, "files"); err != nil || len(got) != 2 || string(got["files/a/shot.png"]) != "png" {
		t.Fatalf("under: %v %v", got, err)
	}

	if got, err := r.FilesBut(ctx, v, "files"); err != nil || len(got) != 2 || got["files/b.txt"] != nil {
		t.Fatalf("but: %v %v", got, err)
	}

	if _, ok, err := r.File(ctx, v, "missing"); err != nil || ok {
		t.Fatalf("a missing file: %v %v", ok, err)
	}

	each, err := r.FileEach(ctx, []string{v, other[conformPrefix+"g"]}, "recording.txt")
	if err != nil || len(each) != 1 || string(each[v]) != "r" {
		t.Fatalf("each: %v %v", each, err)
	}
}

func conformRace(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()

	const n = 12

	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)

	for i := range n {
		s := open(t)

		wg.Go(func() {
			_, err := s.Write(ctx, "race", gitstore.Update{Ref: conformPrefix + "race", Files: files("who", strconv.Itoa(i))})

			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, gitstore.ErrStale):
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("%d winners", wins.Load())
	}
}

func conformManyWriters(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()

	const n = 12

	var wg sync.WaitGroup

	for i := range n {
		s := open(t)

		wg.Go(func() {
			ref := conformPrefix + "many/" + strconv.Itoa(i)
			if _, err := s.Write(ctx, "one of many", gitstore.Update{Ref: ref, Any: true, Files: files("i", strconv.Itoa(i))}); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if refs, err := open(t).List(ctx, conformPrefix+"many/"); err != nil || len(refs) != n {
		t.Fatalf("%d of %d records: %v", len(refs), n, err)
	}
}

func conformVersions(t *testing.T, open func(*testing.T) *gitstore.Store) {
	t.Helper()

	ctx := t.Context()
	s := open(t)
	ref := conformPrefix + "v"

	if _, err := s.Write(ctx, "one", gitstore.Update{Ref: ref, Files: files("v", "1")}); err != nil {
		t.Fatal(err)
	}

	r := open(t)

	first, ok, err := r.First(ctx, []string{ref})
	if err != nil || !ok {
		t.Fatalf("the first version: %v %v", ok, err)
	}

	if _, err := s.Write(ctx, "two", gitstore.Update{Ref: ref, Any: true, Files: files("v", "2")}); err != nil {
		t.Fatal(err)
	}

	// a reader that found the first version reads it whole after the second is written
	data, ok, err := r.File(ctx, first.Commit, "v")
	if err != nil || !ok || string(data) != "1" {
		t.Fatalf("the first version after a second: %q %v %v", data, ok, err)
	}
}

// MemRecords keeps records in memory: the reference a store's Records is held to, and a test double.
type MemRecords struct {
	mu       sync.Mutex
	heads    map[string]gitstore.Ref
	versions map[string]map[string][]byte
	n        int
}

func NewMemRecords() *MemRecords {
	return &MemRecords{heads: map[string]gitstore.Ref{}, versions: map[string]map[string][]byte{}}
}

func (m *MemRecords) List(_ context.Context, prefixes []string) (map[string]gitstore.Ref, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := map[string]gitstore.Ref{}

	for name, ref := range m.heads {
		for _, p := range prefixes {
			if len(name) >= len(p) && name[:len(p)] == p {
				out[name] = ref
			}
		}
	}

	return out, nil
}

func (m *MemRecords) First(_ context.Context, names []string) (gitstore.Ref, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, n := range names {
		if ref, ok := m.heads[n]; ok {
			return ref, true, nil
		}
	}

	return gitstore.Ref{}, false, nil
}

func (m *MemRecords) Read(_ context.Context, versions, paths []string) (map[string]map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := map[string]map[string][]byte{}

	for _, v := range versions {
		fs, ok := m.versions[v]
		if !ok {
			continue
		}

		if paths == nil {
			out[v] = maps.Clone(fs)

			continue
		}

		some := map[string][]byte{}

		for _, p := range paths {
			if data, ok := fs[p]; ok {
				some[p] = data
			}
		}

		out[v] = some
	}

	return out, nil
}

func (m *MemRecords) Write(_ context.Context, _ string, updates []gitstore.Update) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := map[string]string{}

	for _, u := range updates {
		cur, exists := m.heads[u.Ref]

		if !u.Any && ((u.Old == "" && exists) || (u.Old != "" && cur.Commit != u.Old)) {
			return out, gitstore.ErrStale
		}

		if u.Delete {
			delete(m.heads, u.Ref)

			continue
		}

		m.n++
		v := "mem" + strconv.Itoa(m.n)
		m.versions[v] = maps.Clone(u.Files)
		m.heads[u.Ref] = gitstore.Ref{Name: u.Ref, Commit: v, Time: time.Now().UTC().Truncate(time.Second)}
		out[u.Ref] = v
	}

	return out, nil
}

func (m *MemRecords) Close() error { return nil }
