package gitstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
)

func bare(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	// a branch store reads by partial fetches, which the host serves
	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}} {
		if out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	return "file://" + dir
}

// onBranch runs every test of the package again with its stores on a
// branch: the records must behave the same either way.
var onBranch bool

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		onBranch = true
		code = m.Run()
	}

	os.Exit(code)
}

func newStore(t *testing.T, remote string) *Store {
	t.Helper()

	s := &Store{Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch"), Retries: 10}
	if onBranch {
		s.Branch = &branchstore.Store{Remote: remote, Branch: "pipelinegen", Dir: filepath.Join(t.TempDir(), "branch"), Retries: 10}
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}

func files(kv ...string) map[string][]byte {
	m := map[string][]byte{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = []byte(kv[i+1])
	}

	return m
}

const prefix = "refs/pipelinegen/test/"

func TestWriteListRead(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	a, b := newStore(t, remote), newStore(t, remote)

	refs, err := a.List(ctx, prefix)
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty remote: %v %v", refs, err)
	}

	written, err := a.Write(ctx, "two", Update{Ref: prefix + "x/one", Files: files("state.json", "1", "dir/rec.txt", "r")},
		Update{Ref: prefix + "x/two", Files: files("state.json", "2")})
	if err != nil || len(written) != 2 {
		t.Fatalf("write: %v %v", written, err)
	}

	refs, err = b.List(ctx, prefix+"x/", "refs/pipelinegen/other/")
	if err != nil || len(refs) != 2 || refs[prefix+"x/one"].Commit != written[prefix+"x/one"] {
		t.Fatalf("list: %v %v", refs, err)
	}

	if age := time.Since(refs[prefix+"x/one"].Time); age < 0 || age > time.Minute {
		t.Fatalf("time %v", refs[prefix+"x/one"].Time)
	}

	got, err := b.Files(ctx, refs[prefix+"x/one"].Commit)
	if err != nil || string(got["state.json"]) != "1" || string(got["dir/rec.txt"]) != "r" || len(got) != 2 {
		t.Fatalf("files: %q %v", got, err)
	}

	if _, err := a.Write(ctx, "rm", Update{Ref: prefix + "x/two", Old: written[prefix+"x/two"], Delete: true}); err != nil {
		t.Fatal(err)
	}

	if refs, err := b.List(ctx, prefix+"x/"); err != nil || len(refs) != 1 {
		t.Fatalf("after delete: %v %v", refs, err)
	}
}

func TestLeases(t *testing.T) {
	ctx := t.Context()
	s := newStore(t, bare(t))
	ref := prefix + "lease"

	first, err := s.Write(ctx, "create", Update{Ref: ref, Files: files("a", "1")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "create again", Update{Ref: ref, Files: files("a", "2")}); !errors.Is(err, ErrStale) {
		t.Fatalf("second create: %v", err)
	}

	second, err := s.Write(ctx, "swap", Update{Ref: ref, Old: first[ref], Files: files("a", "2")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "stale swap", Update{Ref: ref, Old: first[ref], Files: files("a", "2")}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale swap: %v", err)
	}

	if _, err := s.Write(ctx, "stale delete", Update{Ref: ref, Old: first[ref], Delete: true}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}

	if _, err := s.Write(ctx, "both", Update{Ref: prefix + "new", Files: files("a", "1")},
		Update{Ref: ref, Old: first[ref], Files: files("a", "3")}); !errors.Is(err, ErrStale) {
		t.Fatalf("atomic: %v", err)
	}

	refs, err := s.List(ctx, prefix)
	if err != nil || len(refs) != 1 || refs[ref].Commit != second[ref] {
		t.Fatalf("after the stale writes: %v %v", refs, err)
	}

	if _, err := s.Write(ctx, "any", Update{Ref: ref, Any: true, Files: files("a", "4")}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write(ctx, "unlock", Update{Ref: ref, Any: true, Delete: true}); err != nil {
		t.Fatal(err)
	}

	if refs, err := s.List(ctx, prefix); err != nil || len(refs) != 0 {
		t.Fatalf("%v %v", refs, err)
	}
}

func TestOneCreatorWinsARace(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	const n = 12

	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)

	for i := range n {
		wg.Go(func() {
			s := newStore(t, remote)

			_, err := s.Write(ctx, "race", Update{Ref: prefix + "race", Files: files("who", strconv.Itoa(i))})

			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, ErrStale):
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("%d winners", wins.Load())
	}
}

func TestBadNames(t *testing.T) {
	ctx := t.Context()
	s := newStore(t, bare(t))

	for _, ref := range []string{"heads/x", prefix + "a..b", prefix + "x.lock", prefix + ".hidden", prefix + "sp ace"} {
		if _, err := s.Write(ctx, "bad", Update{Ref: ref, Files: files("a", "1")}); err == nil || errors.Is(err, ErrStale) {
			t.Errorf("%q: %v", ref, err)
		}
	}

	if _, err := s.List(ctx, "refs/x"); err == nil {
		t.Error("a prefix without its slash")
	}
}

func TestTokenStaysOutOfArgumentsAndErrors(t *testing.T) {
	const token = "ghs_secretvalue"

	s := &Store{Remote: "https://git.invalid/o/r.git", Token: token}

	env := strings.Join(s.env(nil), "\n")
	if strings.Contains(env, token) {
		t.Fatal("the token is in the environment in clear")
	}

	if !strings.Contains(env, "GIT_CONFIG_KEY_0=http.https://git.invalid/o/r.git.extraHeader") {
		t.Fatalf("no header config:\n%s", env)
	}

	if got := s.redact("fatal: " + token); strings.Contains(got, token) {
		t.Fatal(got)
	}

	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if got := s.redact("> Authorization: Basic " + basic); strings.Contains(got, basic) {
		t.Fatal(got)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	s.Dir = filepath.Join(t.TempDir(), "scratch")

	_, err := s.List(ctx, prefix)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("list on an unreachable remote: %v", err)
	}
}

func TestCloseRemovesItsOwnScratch(t *testing.T) {
	s := &Store{Remote: bare(t)}
	if _, err := s.List(t.Context(), prefix); err != nil {
		t.Fatal(err)
	}

	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scratch left behind")
	}
}

func TestManyFilesOneFileAndTheNearestRecord(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	w, r := newStore(t, remote), newStore(t, remote)

	many := map[string][]byte{}
	for i := range 500 {
		many[fmt.Sprintf("suite-%d/slice.%03d.txt", i%3, i)] = fmt.Appendf(nil, "pkg#func:F%d\n", i)
	}

	if _, err := w.Write(ctx, "lists", Update{Ref: "refs/pipelinegen/lists/bbb", Files: many},
		Update{Ref: "refs/pipelinegen/lists/ddd", Files: files("a.txt", "d")}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := r.First(ctx, []string{"refs/pipelinegen/lists/aaa", "refs/pipelinegen/lists/bbb", "refs/pipelinegen/lists/ddd"})
	if err != nil || !ok || got.Name != "refs/pipelinegen/lists/bbb" {
		t.Fatalf("first: %+v %v %v", got, ok, err)
	}

	all, err := r.Files(ctx, got.Commit)
	if err != nil || len(all) != 500 || string(all["suite-1/slice.001.txt"]) != "pkg#func:F1\n" {
		t.Fatalf("%d files %v", len(all), err)
	}

	one, ok, err := r.File(ctx, got.Commit, "suite-2/slice.005.txt")
	if err != nil || !ok || string(one) != "pkg#func:F5\n" {
		t.Fatalf("one: %q %v %v", one, ok, err)
	}

	if _, ok, err := r.File(ctx, got.Commit, "nope.txt"); ok || err != nil {
		t.Fatalf("missing file: %v %v", ok, err)
	}

	if _, ok, err := r.First(ctx, []string{"refs/pipelinegen/lists/zzz"}); ok || err != nil {
		t.Fatalf("none: %v %v", ok, err)
	}
}

func TestATokenKeepsTheHardening(t *testing.T) {
	s := &Store{Remote: "https://git.invalid/o/r.git", Token: "ghs_secretvalue"}

	for key, want := range map[string]string{
		"core.hooksPath": os.DevNull,
		"core.fsmonitor": "false",
		"http.https://git.invalid/o/r.git.extraheader": "Authorization: Basic ",
	} {
		cmd := exec.CommandContext(t.Context(), "git", "config", "--get", key)
		cmd.Env, cmd.Dir = s.env(nil), t.TempDir()

		out, err := cmd.Output()
		if got := strings.TrimSpace(string(out)); err != nil || !strings.HasPrefix(got, want) {
			t.Errorf("%s = %q, want %q: %v", key, got, want, err)
		}
	}
}
