package branchstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func remote(t *testing.T, hook string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "r.git")
	run(t, "", "git", "init", "-q", "--bare", dir)

	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}, {"core.logAllRefUpdates", "always"}} {
		run(t, dir, "git", "config", kv[0], kv[1])
	}

	if hook != "" {
		p := filepath.Join(dir, "hooks", "pre-receive")
		if err := testenv.WriteExecutable(t.Context(), p, []byte("#!/bin/sh\n"+hook+"\n")); err != nil {
			t.Fatal(err)
		}
	}

	return "file://" + dir
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}

	return string(out)
}

func open(t *testing.T, url string) *Store {
	t.Helper()

	s := &Store{Remote: url, Branch: "pipelinegen", Dir: filepath.Join(t.TempDir(), "scratch"), Retries: 3}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}

func read(t *testing.T, s *Store, p string) (string, bool) {
	t.Helper()

	v, err := s.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	data, ok, err := v.Get(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data), ok
}

func TestWriteThenReadElsewhere(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")

	empty, err := open(t, url).Snapshot(ctx)
	if err != nil {
		t.Fatalf("a branch not made yet: %v", err)
	}

	if empty.Tip() != "" {
		t.Fatalf("a branch not made yet has the tip %s", empty.Tip())
	}

	w := open(t, url)
	if err := w.Write(ctx, "first", Change{Files: map[string][]byte{
		"runs/a/plan.json": []byte(`{"a":1}`), "runs/a/e2e/result.json": []byte("ok"), "locks/x.json": []byte("{}"),
	}}); err != nil {
		t.Fatal(err)
	}

	r := open(t, url)

	v, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := v.List(ctx, "runs/a"); err != nil || !slices.Equal(got, []string{"e2e", "plan.json"}) {
		t.Errorf("list runs/a: %v", got)
	}

	if got, err := v.Files(ctx, "runs"); err != nil || !slices.Equal(got, []string{"runs/a/e2e/result.json", "runs/a/plan.json"}) {
		t.Errorf("files under runs: %v", got)
	}

	if got, err := v.List(ctx, "nothing/here"); err != nil || len(got) != 0 {
		t.Errorf("a missing directory lists %v", got)
	}

	got, err := v.Read(ctx, []string{"runs/a/plan.json", "missing.json"})
	if err != nil || len(got) != 1 || string(got["runs/a/plan.json"]) != `{"a":1}` {
		t.Fatalf("read: %q %v", got, err)
	}

	if out := run(t, r.Dir, "git", "rev-parse", "--is-shallow-repository"); strings.TrimSpace(out) != "true" {
		t.Errorf("the reader's fetch was not shallow: %s", out)
	}
}

func TestDeletesRemoveFilesAndDirectories(t *testing.T) {
	ctx := t.Context()
	s := open(t, remote(t, ""))

	if err := s.Write(ctx, "seed", Change{Files: map[string][]byte{"a/1": []byte("1"), "a/b/2": []byte("2"), "c": []byte("3")}}); err != nil {
		t.Fatal(err)
	}

	if err := s.Write(ctx, "drop", Change{Deletes: []string{"a", "c"}, Files: map[string][]byte{"d": []byte("4")}}); err != nil {
		t.Fatal(err)
	}

	v, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := v.Files(ctx, ""); err != nil || !slices.Equal(got, []string{"d"}) {
		t.Fatalf("after the deletes: %v", got)
	}
}

func TestAWriteTheTipHoldsWritesNothing(t *testing.T) {
	ctx := t.Context()
	s := open(t, remote(t, ""))
	c := Change{Files: map[string][]byte{"objects/ab/cdef": []byte("x")}, Deletes: []string{"gone"}}

	if err := s.Write(ctx, "once", c); err != nil {
		t.Fatal(err)
	}

	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Write(ctx, "again", c); err != nil {
		t.Fatal(err)
	}

	after, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if before.Tip() != after.Tip() {
		t.Fatal("writing what the tip holds made a commit")
	}
}

func TestBadPathsAreRefused(t *testing.T) {
	s := open(t, remote(t, ""))

	for _, p := range []string{"", "/abs", "a/../b", "a//b", ".git/config", "dir/"} {
		if err := s.Write(t.Context(), "bad", Change{Files: map[string][]byte{p: nil}}); !errors.Is(err, ErrPath) {
			t.Errorf("%q: %v", p, err)
		}

		if err := s.Write(t.Context(), "bad", Change{Deletes: []string{p}}); !errors.Is(err, ErrPath) {
			t.Errorf("delete %q: %v", p, err)
		}
	}
}

func TestALostRaceIsReplayed(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")
	s, other := open(t, url), open(t, url)

	calls := 0

	err := s.Update(ctx, "mine", func(ctx context.Context, _ *View) (Change, error) {
		calls++
		if calls == 1 {
			if err := other.Write(ctx, "theirs", Change{Files: map[string][]byte{"theirs": []byte("t")}}); err != nil {
				return Change{}, err
			}
		}

		return Change{Files: map[string][]byte{"mine": []byte("m")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if calls != 2 || s.Replays() != 1 || s.Retried() != 0 {
		t.Errorf("decided %d times, %d replays, %d retries; want 2, 1, 0", calls, s.Replays(), s.Retried())
	}

	for _, p := range []string{"mine", "theirs"} {
		if _, ok := read(t, open(t, url), p); !ok {
			t.Errorf("%s was lost", p)
		}
	}
}

const writers = 20

// slowHost is a pre-receive hook that holds every push a while, as a far host does: two pushes may
// both pass it, and the later one then fails as its branch moved.
const slowHost = "sleep 0.2"

func TestTwentyWritersAllLand(t *testing.T) {
	ctx := t.Context()
	url := remote(t, slowHost)

	// the branch exists first, as it does once a repository has planned
	if err := open(t, url).Write(ctx, "start", Change{Files: map[string][]byte{"start": nil}}); err != nil {
		t.Fatal(err)
	}

	stores := make([]*Store, writers)
	for i := range stores {
		stores[i] = open(t, url)
	}

	start := time.Now()
	errs := make([]error, writers)

	var wg sync.WaitGroup

	for i, s := range stores {
		wg.Go(func() {
			p := fmt.Sprintf("runs/r1/e2e/batch-%02d.json", i)
			errs[i] = s.Write(ctx, "batch "+strconv.Itoa(i), Change{Files: map[string][]byte{p: []byte(strconv.Itoa(i))}})
		})
	}

	wg.Wait()

	replays, most := 0, 0

	for i, s := range stores {
		if errs[i] != nil {
			t.Errorf("writer %d: %v", i, errs[i])
		}

		if s.Retried() != 0 {
			t.Errorf("writer %d counted %d lost races as retries", i, s.Retried())
		}

		replays += s.Replays()
		most = max(most, s.Replays())
	}

	t.Logf("%d writers: %d replays in all, at most %d for one, %s", writers, replays, most, time.Since(start).Round(time.Millisecond))

	// what counts is the remote's tree, read there, not what the writers
	// believe
	for i := range writers {
		p := fmt.Sprintf("runs/r1/e2e/batch-%02d.json", i)
		if got, ok := onRemote(t, url, p); !ok || got != strconv.Itoa(i) {
			t.Errorf("%s on the remote: %q, %v", p, got, ok)
		}
	}
}

func onRemote(t *testing.T, url, p string) (string, bool) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", "--git-dir", strings.TrimPrefix(url, "file://"), "cat-file", "blob", "refs/heads/pipelinegen:"+p)

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	return string(out), true
}

func TestTwentyUpdatesDecideAgain(t *testing.T) {
	for _, roots := range []bool{false, true} {
		t.Run(fmt.Sprintf("roots=%v", roots), func(t *testing.T) {
			ctx := t.Context()
			url := remote(t, slowHost)

			var wg sync.WaitGroup

			errs := make([]error, writers)

			for i := range writers {
				s := open(t, url)
				s.Roots = roots

				wg.Go(func() {
					errs[i] = s.Update(ctx, "count", func(ctx context.Context, v *View) (Change, error) {
						data, _, err := v.Get(ctx, "count")
						if err != nil {
							return Change{}, err
						}

						n, err := count(data)

						return Change{Files: map[string][]byte{"count": []byte(strconv.Itoa(n + 1))}}, err
					})
				})
			}

			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Errorf("writer %d: %v", i, err)
				}
			}

			if got, _ := onRemote(t, url, "count"); got != strconv.Itoa(writers) {
				t.Fatalf("the remote's count is %s, want %d", got, writers)
			}

			if got, _ := read(t, open(t, url), "count"); got != strconv.Itoa(writers) {
				t.Fatalf("a reader reads the count as %s, want %d", got, writers)
			}

			commits := strings.Count(run(t, "", "git", "--git-dir", strings.TrimPrefix(url, "file://"), "rev-list", "refs/heads/pipelinegen"), "\n")
			if want := map[bool]int{false: writers, true: 1}[roots]; commits != want {
				t.Errorf("the branch has %d commits, want %d", commits, want)
			}
		})
	}
}

func TestARefusedPushIsTriedAgain(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	flag := filepath.Join(dir, "refused")

	// the first push is refused, the next ones pass
	url := remote(t, fmt.Sprintf("if [ ! -e %q ]; then touch %q; echo refused >&2; exit 1; fi", flag, flag))

	s := open(t, url)
	if err := s.Write(ctx, "one", Change{Files: map[string][]byte{"a": []byte("a")}}); err != nil {
		t.Fatal(err)
	}

	if s.Retried() != 1 || s.Replays() != 0 {
		t.Errorf("%d retries, %d replays; want 1, 0", s.Retried(), s.Replays())
	}

	never := open(t, remote(t, "exit 1"))
	never.Retries = 2

	if err := never.Write(ctx, "one", Change{Files: map[string][]byte{"a": []byte("a")}}); err == nil || never.Retried() != 2 {
		t.Errorf("a host that always refuses: %v after %d retries", err, never.Retried())
	}
}

func TestTheDeadlineEndsReplays(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")
	s, other := open(t, url), open(t, url)
	s.Deadline = time.Millisecond

	n := 0

	err := s.Update(ctx, "mine", func(ctx context.Context, _ *View) (Change, error) {
		n++
		if err := other.Write(ctx, "theirs", Change{Files: map[string][]byte{"theirs": []byte(strconv.Itoa(n))}}); err != nil {
			return Change{}, err
		}

		return Change{Files: map[string][]byte{"mine": nil}}, nil
	})
	if !errors.Is(err, ErrDeadline) {
		t.Fatalf("got %v, want the deadline", err)
	}
}

func TestTheSameChangeTwiceCountsTwice(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")
	second := func() time.Time { return time.Unix(1_800_000_000, 0) }

	a, b := open(t, url), open(t, url)
	a.Clock, b.Clock = second, second

	increment := func(ctx context.Context, v *View) (Change, error) {
		data, _, err := v.Get(ctx, "count")
		if err != nil {
			return Change{}, err
		}

		n, err := count(data)

		return Change{Files: map[string][]byte{"count": []byte(strconv.Itoa(n + 1))}}, err
	}

	first := true

	err := b.Update(ctx, "count", func(ctx context.Context, v *View) (Change, error) {
		c, err := increment(ctx, v)

		if first {
			first = false

			if err := a.Update(ctx, "count", increment); err != nil {
				return Change{}, err
			}
		}

		return c, err
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := onRemote(t, url, "count"); got != "2" {
		t.Fatalf("the remote's count is %q after two increments", got)
	}
}

func count(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	n, err := strconv.Atoi(string(data))
	if err != nil {
		return 0, fmt.Errorf("count %q: %w", data, err)
	}

	return n, nil
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

func TestAWriterReplacesAnothersFilesItNeverRead(t *testing.T) {
	ctx := t.Context()

	for _, roots := range []bool{false, true} {
		url := remote(t, "")

		first := open(t, url)
		first.Roots = roots

		if err := first.Write(ctx, "first", Change{Files: map[string][]byte{"a/state.json": []byte("running"), "b/state.json": []byte("pending")}}); err != nil {
			t.Fatal(err)
		}

		second := open(t, url)
		second.Roots = roots

		if err := second.Write(ctx, "second", Change{Files: map[string][]byte{"a/state.json": []byte("passed")}}); err != nil {
			t.Fatalf("roots %v: %v", roots, err)
		}

		for p, want := range map[string]string{"a/state.json": "passed", "b/state.json": "pending"} {
			if got, ok := read(t, open(t, url), p); !ok || got != want {
				t.Errorf("roots %v: %s reads %q", roots, p, got)
			}
		}
	}
}

func TestReadsAndWritesOverOthersOnAnOldGit(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")

	if err := open(t, url).Write(ctx, "first", Change{Files: map[string][]byte{"a/state.json": []byte("running"), "b/state.json": []byte("pending")}}); err != nil {
		t.Fatal(err)
	}

	testenv.OldGit(t)

	s := open(t, url)
	if got, ok := read(t, s, "b/state.json"); !ok || got != "pending" {
		t.Fatalf("read %q %v", got, ok)
	}

	if err := s.Write(ctx, "second", Change{Files: map[string][]byte{"a/state.json": []byte("passed")}}); err != nil {
		t.Fatal(err)
	}

	if got, ok := read(t, open(t, url), "a/state.json"); !ok || got != "passed" {
		t.Errorf("reads %q", got)
	}
}

// hidesTheBranchOnce is a receive-pack that leaves the branch out of its first answer, as a remote
// does whose filesystem lists a ref being replaced by another push as no ref at all.
func hidesTheBranchOnce(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	p := filepath.Join(dir, "receive-pack")
	script := "#!/bin/sh\nif mkdir '" + filepath.Join(dir, "once") + "' 2>/dev/null; then\n" +
		"\texec git -c receive.hideRefs=refs/heads/pipelinegen receive-pack \"$@\"\nfi\nexec git receive-pack \"$@\"\n"

	if err := testenv.WriteExecutable(t.Context(), p, []byte(script)); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestABranchMissingFromThePushsAnswerIsALostRace(t *testing.T) {
	ctx := t.Context()
	url := remote(t, "")

	if err := open(t, url).Write(ctx, "first", Change{Files: map[string][]byte{"a/state.json": []byte("running")}}); err != nil {
		t.Fatal(err)
	}

	s := open(t, url)
	if _, err := s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}

	run(t, s.Dir, "git", "config", "remote."+remoteName+".receivepack", hidesTheBranchOnce(t))

	// pushed as a new branch, the commit would carry a.json, which this store never fetched
	if err := s.Write(ctx, "second", Change{Files: map[string][]byte{"b/state.json": []byte("pending")}}); err != nil {
		t.Fatal(err)
	}

	if s.Replays() != 1 || s.Retried() != 0 {
		t.Errorf("%d replays, %d retries; want 1, 0", s.Replays(), s.Retried())
	}

	for p, want := range map[string]string{"a/state.json": "running", "b/state.json": "pending"} {
		if got, ok := read(t, open(t, url), p); !ok || got != want {
			t.Errorf("%s reads %q, %v", p, got, ok)
		}
	}
}
