// Package branchstore keeps pipelinegen's records as files on a branch that shares no history with
// the code: a write is a commit on the tip it read, applied again by paths when the tip moved first.
package branchstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/internal/tries"
)

// Store is one branch of a remote repository, read and written through a
// scratch repository of its own.
type Store struct {
	// Remote is the repository's URL. It never carries a token.
	Remote string
	// Token, when set, authenticates to an HTTPS remote as a GitHub token,
	// through git's environment only.
	Token  string
	Env    []string
	Branch string
	Dir    string
	Clock  func() time.Time
	// Retries is how often a write that failed in passing goes again at least (0: once); a remote's
	// refusal goes again only a few times, and a push that lost a race isn't counted.
	Retries  int
	Backoff  time.Duration
	RetryFor time.Duration
	Until    time.Time
	Deadline time.Duration
	// Roots makes every write a parentless commit pushed in place of the tip it read, so the branch
	// keeps no history: for small content whose past is of no use.
	Roots bool

	mu      sync.Mutex
	temp    bool
	ready   bool
	retried atomic.Int64
	replays atomic.Int64
}

// Tries sets how a write goes again, between writes: a store that wraps
// this one keeps its own settings in step.
func (s *Store) Tries(p tries.Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Retries, s.Backoff, s.RetryFor, s.Until = p.Retries, p.First, p.Deadline, p.Until
}

// DefaultDeadline is how long a write replays after lost races when the
// store names no deadline.
const DefaultDeadline = 10 * time.Minute

var ErrGit = errors.New("git")

var ErrDeadline = errors.New("the branch kept moving under the write")

const (
	blobKind    = "blob"
	remoteName  = "store"
	author      = "pipelinegen"
	authorEmail = "pipelinegen@users.noreply.github.com"
	zeroID      = "0000000000000000000000000000000000000000"
)

// Retried says how many tries again the store's writes needed after
// refusals and transport failures; Replays how many after lost races.
func (s *Store) Retried() int { return int(s.retried.Load()) }

func (s *Store) Replays() int { return int(s.replays.Load()) }

func (s *Store) local() string { return "refs/remotes/" + remoteName + "/" + s.Branch }

// Close removes the scratch repository when the store made it.
func (s *Store) Close() error {
	if !s.temp || s.Dir == "" {
		return nil
	}

	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("remove scratch repository: %w", err)
	}

	s.Dir, s.temp, s.ready = "", false, false

	return nil
}

func (s *Store) init(ctx context.Context) error {
	if s.ready {
		return nil
	}

	if s.Remote == "" || s.Branch == "" {
		return fmt.Errorf("%w: a store needs a remote and a branch", ErrGit)
	}

	if s.Dir == "" {
		dir, err := os.MkdirTemp("", "pipelinegen-branch-")
		if err != nil {
			return fmt.Errorf("scratch repository: %w", err)
		}

		s.Dir, s.temp = dir, true
	}

	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	if _, err := s.git(ctx, nil, nil, "check-ref-format", "refs/heads/"+s.Branch); err != nil {
		return fmt.Errorf("%w: bad branch name %q", ErrGit, s.Branch)
	}

	if _, err := os.Stat(filepath.Join(s.Dir, "HEAD")); err != nil {
		// a partial clone's remote: file contents come when read, so a
		// writer never downloads what it doesn't read
		const config = "config"

		for _, args := range [][]string{
			{"init", "--quiet", "--bare"},
			{"remote", "add", remoteName, s.Remote},
			{config, "remote." + remoteName + ".promisor", "true"},
			{config, "remote." + remoteName + ".partialclonefilter", "blob:none"},
			{config, "extensions.partialClone", remoteName},
			{config, "fetch.writeCommitGraph", "false"},
			{config, "gc.auto", "0"},
			// a push leaves out every file its parent holds, at whatever
			// path; git's sparse walk proves that path by path only
			{config, "pack.useSparse", "false"},
		} {
			if _, err := s.git(ctx, nil, nil, args...); err != nil {
				return err
			}
		}
	}

	s.ready = true

	return nil
}

// env is what every git command runs with: no prompts, a fixed author, and
// the token as an HTTP header set through git's environment config.
func (s *Store) env(extra []string) []string {
	vars := []string{
		"GIT_AUTHOR_NAME=" + author, "GIT_AUTHOR_EMAIL=" + authorEmail,
		"GIT_COMMITTER_NAME=" + author, "GIT_COMMITTER_EMAIL=" + authorEmail,
	}

	if s.Token != "" {
		secret.Add(s.Token)

		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + s.Token))
		// the hardening's settings go after these, never over them
		vars = append(vars,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http."+s.Remote+".extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
			// a credential helper from the user's config would answer
			// before the header is tried
			"GIT_CONFIG_KEY_1=credential.helper",
			"GIT_CONFIG_VALUE_1=",
		)
	}

	return append(append(secret.GitEnv(vars...), s.Env...), extra...)
}

type gitError struct {
	args   []string
	err    error
	stderr string
	stdout string
}

func (e *gitError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", ErrGit, strings.Join(e.args, " "), e.err, strings.TrimSpace(e.stderr))
}

func (e *gitError) Unwrap() error { return ErrGit }

func (s *Store) git(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with the store's own arguments
	cmd.Dir = s.Dir
	cmd.Env = s.env(env)
	cmd.Stdin = stdin

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", &gitError{args: brief(args), err: err, stderr: s.redact(stderr.String()), stdout: s.redact(stdout.String())}
	}

	return stdout.String(), nil
}

func brief(args []string) []string {
	if len(args) <= 8 {
		return args
	}

	return append(slices.Clone(args[:7]), fmt.Sprintf("(%d more)", len(args)-7))
}

func (s *Store) redact(text string) string {
	if s.Token != "" {
		secret.Add(s.Token)
	}

	return secret.Redact(text)
}

const transportTries = 3

func (s *Store) fetch(ctx context.Context) (string, error) {
	var err error

	for try := range transportTries {
		var out string

		_, err = s.git(ctx, nil, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--depth", "1",
			"--filter=blob:none", remoteName, "+refs/heads/"+s.Branch+":"+s.local())
		if ge, ok := errors.AsType[*gitError](err); ok && strings.Contains(ge.stderr, "couldn't find remote ref") {
			return "", nil
		}

		if err == nil {
			out, err = s.git(ctx, nil, nil, "rev-parse", "--verify", "--quiet", s.local()+"^{commit}")
			if err == nil {
				return strings.TrimSpace(out), nil
			}
		}

		if ctx.Err() != nil {
			break
		}

		s.pause(ctx, time.Duration(try+1)*200*time.Millisecond)
	}

	return "", fmt.Errorf("%w: %w", errTransport, err)
}

var errTransport = errors.New("fetching the branch failed")

// replayWait is the wait before the n-th replay after a lost race: random up to a bound that
// doubles with every loss, so writers that collided spread out.
func replayWait(n int) time.Duration {
	bound := replayBase << min(n-1, 6)

	return time.Duration(mrand.Int64N(int64(min(bound, replayCap)))) //nolint:gosec // jitter, not a secret
}

const (
	replayBase = 100 * time.Millisecond
	replayCap  = 5 * time.Second
)

func (s *Store) wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)

	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Store) pause(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}

	t := time.NewTimer(d + time.Duration(mrand.Int64N(int64(d)))) //nolint:gosec // jitter, not a secret

	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// View is the branch as one fetch found it: every read of a view sees the
// same tip.
type View struct {
	s   *Store
	tip string
}

// Snapshot fetches the branch's tip. A branch that doesn't exist yet is an
// empty view.
func (s *Store) Snapshot(ctx context.Context) (*View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.snapshot(ctx)
}

func (s *Store) snapshot(ctx context.Context) (*View, error) {
	if err := s.init(ctx); err != nil {
		return nil, err
	}

	tip, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}

	return &View{s: s, tip: tip}, nil
}

// Tip is the commit the view reads; empty for a branch not made yet.
func (v *View) Tip() string { return v.tip }

type entry struct {
	mode, kind, id string
}

func (v *View) ls(ctx context.Context, dir string, deep bool) (map[string]entry, error) {
	out := map[string]entry{}
	if v.tip == "" {
		return out, nil
	}

	args := []string{"ls-tree", "-z"}
	if deep {
		args = append(args, "-r")
	}

	spec := v.tip
	if dir = strings.Trim(dir, "/"); dir != "" {
		spec += ":" + dir
	}

	// a directory that isn't there lists nothing
	if _, err := v.s.git(ctx, nil, nil, "cat-file", "-e", spec); err != nil {
		return out, nil //nolint:nilerr // absent, not broken
	}

	text, err := v.s.git(ctx, nil, nil, append(args, spec)...)
	if err != nil {
		return nil, err
	}

	for line := range strings.SplitSeq(text, "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}

		f := strings.Fields(meta)
		if len(f) != 3 {
			continue
		}

		p := name
		if dir != "" {
			p = dir + "/" + name
		}

		out[p] = entry{mode: f[0], kind: f[1], id: f[2]}
	}

	return out, nil
}

// List names what a directory holds, files and directories, by their
// names inside it, sorted; none for a directory that isn't there.
func (v *View) List(ctx context.Context, dir string) ([]string, error) {
	got, err := v.ls(ctx, dir, false)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(got))
	for p := range got {
		out = append(out, path.Base(p))
	}

	slices.Sort(out)

	return out, nil
}

// Files names every file under a directory, by its path from the root,
// sorted.
func (v *View) Files(ctx context.Context, dir string) ([]string, error) {
	got, err := v.ls(ctx, dir, true)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(got))
	for p, e := range got {
		if e.kind == blobKind {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out, nil
}

func (v *View) Get(ctx context.Context, p string) ([]byte, bool, error) {
	got, err := v.Read(ctx, []string{p})
	if err != nil {
		return nil, false, err
	}

	data, ok := got[p]

	return data, ok, nil
}

// Read reads files, fetching what the scratch repository lacks in one round trip; paths the view
// lacks are left out, and one its tree names is read or an error, never taken as missing.
func (v *View) Read(ctx context.Context, paths []string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if v.tip == "" || len(paths) == 0 {
		return files, nil
	}

	for _, p := range paths {
		if err := checkPath(p); err != nil {
			return nil, err
		}
	}

	have, err := v.entries(ctx, paths)
	if err != nil {
		return nil, err
	}

	var ids []string

	byID := map[string][]string{}

	for _, p := range paths {
		if e, ok := have[p]; ok && e.kind == blobKind {
			if _, seen := byID[e.id]; !seen {
				ids = append(ids, e.id)
			}

			byID[e.id] = append(byID[e.id], p)
		}
	}

	if len(ids) == 0 {
		return files, nil
	}

	if err := v.s.fetchMissing(ctx, ids); err != nil {
		return nil, err
	}

	got, err := v.s.blobsByID(ctx, ids)
	if err != nil {
		return nil, err
	}

	for id, data := range got {
		for _, p := range byID[id] {
			files[p] = data
		}
	}

	return files, nil
}

// fetchMissing fetches in one go the contents of ids the scratch repository lacks, learned from the
// objects it holds: a git before 2.45 dies when asked of a missing object with lazy fetch off.
func (s *Store) fetchMissing(ctx context.Context, ids []string) error {
	want, err := s.lacking(ctx, ids)
	if err != nil || len(want) == 0 {
		return err
	}

	if _, err := s.git(ctx, nil, strings.NewReader(strings.Join(want, "\n")+"\n"), "-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet", "--no-tags",
		"--no-write-fetch-head", "--recurse-submodules=no", "--stdin", remoteName); err != nil {
		return err
	}

	still, err := s.lacking(ctx, want)
	if err != nil {
		return err
	}

	if len(still) > 0 {
		return fmt.Errorf("%w: the remote gave none of %s", ErrGit, strings.Join(still, ", "))
	}

	return nil
}

func (s *Store) lacking(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	out, err := s.git(ctx, []string{noLazyFetch}, nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}

	have := map[string]bool{}
	for id := range strings.FieldsSeq(out) {
		have[id] = true
	}

	var want []string

	for _, id := range ids {
		if !have[id] && !slices.Contains(want, id) {
			want = append(want, id)
		}
	}

	return want, nil
}

func (s *Store) blobsByID(ctx context.Context, ids []string) (map[string][]byte, error) {
	var in bytes.Buffer
	for _, id := range ids {
		in.WriteString(id + "\n")
	}

	out, err := s.git(ctx, []string{noLazyFetch}, &in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	r := bufio.NewReader(strings.NewReader(out))
	files := make(map[string][]byte, len(ids))

	for range ids {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%w: cat-file output ends early", ErrGit)
		}

		f := strings.Fields(header)
		if len(f) != 3 || f[1] != blobKind {
			return nil, fmt.Errorf("%w: a file's content didn't come: cat-file says %q", ErrGit, strings.TrimSpace(header))
		}

		size, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("%w: cat-file size %q", ErrGit, f[2])
		}

		data := make([]byte, size+1)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("%w: cat-file output ends early", ErrGit)
		}

		files[f[0]] = data[:size]
	}

	return files, nil
}

// noLazyFetch keeps git from fetching a missing content on its own, one
// round trip per file.
const noLazyFetch = "GIT_NO_LAZY_FETCH=1"

func (v *View) entries(ctx context.Context, paths []string) (map[string]entry, error) {
	out := map[string]entry{}
	if v.tip == "" || len(paths) == 0 {
		return out, nil
	}

	text, err := v.s.git(ctx, []string{"GIT_LITERAL_PATHSPECS=1", noLazyFetch}, nil,
		append([]string{"ls-tree", "-r", "-z", v.tip, "--"}, paths...)...)
	if err != nil {
		return nil, err
	}

	for line := range strings.SplitSeq(text, "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 {
			out[name] = entry{mode: f[0], kind: f[1], id: f[2]}
		}
	}

	return out, nil
}

var ErrPath = errors.New("bad path")

func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsAny(p, "\n\t\x00") {
		return fmt.Errorf("%w: %q", ErrPath, p)
	}

	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return fmt.Errorf("%w: %q", ErrPath, p)
		}
	}

	return nil
}

// Change is what one write does: files written (whole) and paths removed
// (a file, or a directory with all it holds).
type Change struct {
	Files   map[string][]byte
	Deletes []string
}

func (c Change) Empty() bool { return len(c.Files) == 0 && len(c.Deletes) == 0 }

// Write applies a change that owns its paths, again unchanged on the newer tip after a lost race; a
// change the tip already holds writes nothing, so a push that landed unanswered ends there.
func (s *Store) Write(ctx context.Context, message string, c Change) error {
	return s.Update(ctx, message, func(context.Context, *View) (Change, error) { return c, nil })
}

// Update applies the change decide makes from the branch, deciding again on the newer tip after
// every lost race, so decide must be safe to call twice; an empty change writes nothing.
func (s *Store) Update(ctx context.Context, message string, decide func(context.Context, *View) (Change, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	deadline := s.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}

	if end := tries.HardEnd(ctx, s.Until); !end.IsZero() && time.Until(end) < deadline {
		deadline = time.Until(end)
	}

	start, lost := time.Now(), 0
	run := tries.Policy{Retries: s.Retries, First: s.Backoff, Deadline: s.RetryFor, Until: s.Until}.Start()

	for {
		v, commit, err := s.prepare(ctx, message, decide)

		// a fetch lost in transport goes again as a refused push does
		if errors.Is(err, errTransport) {
			if !run.Again(ctx, tries.Of(err.Error())) {
				return run.Err(err)
			}

			s.retried.Add(1)

			continue
		}

		if err != nil || commit == "" {
			return err
		}

		raced, err := s.push(ctx, v.tip, commit)
		if err == nil {
			return nil
		}

		switch {
		case ctx.Err() != nil:
			return err
		case errors.Is(err, ErrMissing):
			return run.Err(err)
		case raced && time.Since(start) >= deadline:
			return fmt.Errorf("%w for %s: %w", ErrDeadline, deadline, err)
		case raced:
			lost++

			s.replays.Add(1)
			s.wait(ctx, replayWait(lost))
		case !run.Again(ctx, tries.Of(err.Error())):
			return run.Err(err)
		default:
			s.retried.Add(1)
		}
	}
}

func (s *Store) commit(ctx context.Context, v *View, message string, c Change) (string, error) {
	if c.Empty() {
		return "", nil
	}

	index, err := s.tempIndex()
	if err != nil {
		return "", err
	}

	defer os.Remove(index) //nolint:errcheck // a leftover index in the scratch repository harms nothing

	env := []string{"GIT_INDEX_FILE=" + index, noLazyFetch}

	if v.tip != "" {
		if _, err := s.git(ctx, env, nil, "read-tree", v.tip); err != nil {
			return "", err
		}
	}

	info, changed, err := s.edits(ctx, v, c)
	if err != nil || !changed {
		return "", err
	}

	if _, err := s.git(ctx, env, info, "update-index", "--add", "--remove", "--index-info"); err != nil {
		return "", err
	}

	// the contents of files this write doesn't touch stay on the remote
	tree, err := s.git(ctx, env, nil, "write-tree", "--missing-ok")
	if err != nil {
		return "", err
	}

	// a nonce makes every commit unique: two writers deciding the same change on the same tip in the
	// same second would make one commit, and the second push would take the first's for its own
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	args := []string{"commit-tree", strings.TrimSpace(tree), "-m", message, "-m", "nonce " + hex.EncodeToString(nonce)}
	if v.tip != "" && !s.Roots {
		args = append(args, "-p", v.tip)
	}

	var dated []string

	if s.Clock != nil {
		date := strconv.FormatInt(s.Clock().Unix(), 10) + " +0000"
		dated = []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	}

	out, err := s.git(ctx, dated, nil, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

func (s *Store) edits(ctx context.Context, v *View, c Change) (*bytes.Buffer, bool, error) {
	var info bytes.Buffer

	changed := false

	names := make([]string, 0, len(c.Files))
	for p := range c.Files {
		if err := checkPath(p); err != nil {
			return nil, false, err
		}

		names = append(names, p)
	}

	slices.Sort(names)

	ids, err := s.blobs(ctx, names, c.Files)
	if err != nil {
		return nil, false, err
	}

	for _, d := range c.Deletes {
		if err := checkPath(d); err != nil {
			return nil, false, err
		}
	}

	have, err := v.entries(ctx, append(slices.Clone(names), c.Deletes...))
	if err != nil {
		return nil, false, err
	}

	written := map[string]bool{}

	for i, p := range names {
		written[p] = true

		if e, ok := have[p]; ok && e.id == ids[i] {
			continue
		}

		changed = true

		// a file where a directory stood replaces the directory
		for q := range have {
			if strings.HasPrefix(q, p+"/") {
				fmt.Fprintf(&info, "0 %s\t%s\n", zeroID, q)
			}
		}

		fmt.Fprintf(&info, "100644 %s\t%s\n", ids[i], p)
	}

	for _, d := range c.Deletes {
		for q := range have {
			if (q == d || strings.HasPrefix(q, d+"/")) && !written[q] {
				changed = true

				fmt.Fprintf(&info, "0 %s\t%s\n", zeroID, q)
			}
		}
	}

	return &info, changed, nil
}

func (s *Store) blobs(ctx context.Context, names []string, files map[string][]byte) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	dir, err := os.MkdirTemp(s.Dir, "blobs-")
	if err != nil {
		return nil, fmt.Errorf("blobs: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover in the scratch repository harms nothing

	var list bytes.Buffer

	for i, p := range names {
		f := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(f, files[p], 0o600); err != nil {
			return nil, fmt.Errorf("blobs: %w", err)
		}

		list.WriteString(f + "\n")
	}

	out, err := s.git(ctx, nil, &list, "hash-object", "-w", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}

	ids := strings.Fields(out)
	if len(ids) != len(names) {
		return nil, fmt.Errorf("%w: hash-object gave %d ids for %d files", ErrGit, len(ids), len(names))
	}

	return ids, nil
}

func (s *Store) tempIndex() (string, error) {
	f, err := os.CreateTemp(s.Dir, "index-")
	if err != nil {
		return "", fmt.Errorf("index: %w", err)
	}

	p := f.Name()

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("index: %w", err)
	}

	// git refuses an empty file as an index; it wants none at all
	if err := os.Remove(p); err != nil {
		return "", fmt.Errorf("index: %w", err)
	}

	return p, nil
}

func (s *Store) prepare(ctx context.Context, message string, decide func(context.Context, *View) (Change, error)) (*View, string, error) {
	v, err := s.snapshot(ctx)
	if err != nil {
		return nil, "", err
	}

	c, err := decide(ctx, v)
	if err != nil {
		return nil, "", err
	}

	commit, err := s.commit(ctx, v, message, c)
	if err != nil || commit == "" {
		return v, "", err
	}

	return v, commit, s.fetchBases(ctx, v.tip, commit)
}

// fetchBases fetches what a push of commit on tip reads that is missing: a thin push (git's only
// kind over HTTP) reads each replaced file as a delta base, and a Roots commit sends every file.
func (s *Store) fetchBases(ctx context.Context, tip, commit string) error {
	var ids []string

	if tip != "" {
		out, err := s.git(ctx, []string{noLazyFetch}, nil, "diff-tree", "-r", "--no-renames", tip, commit)
		if err != nil {
			return err
		}

		// :<old mode> <new mode> <old id> <new id> M, for a file replaced
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			meta, _, _ := strings.Cut(line, "\t")
			if f := strings.Fields(meta); len(f) == 5 && f[4] == "M" {
				ids = append(ids, f[2])
			}
		}
	}

	if tip == "" || s.Roots {
		out, err := s.git(ctx, []string{noLazyFetch}, nil, "ls-tree", "-r", commit)
		if err != nil {
			return err
		}

		// <mode> blob <id>\t<path>
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			meta, _, _ := strings.Cut(line, "\t")
			if f := strings.Fields(meta); len(f) == 3 && f[1] == blobKind {
				ids = append(ids, f[2])
			}
		}
	}

	if len(ids) == 0 {
		return nil
	}

	if err := s.fetchMissing(ctx, ids); err != nil {
		return fmt.Errorf("%w: the files the push reads: %w", errTransport, err)
	}

	return nil
}

// ErrMissing is a push that needed an object the store did not make and
// never fetched: a defect of pipelinegen, never retried.
var ErrMissing = errors.New("a defect of pipelinegen, please report it: a push needed an object the store never had")

var missingRE = regexp.MustCompile(`could not fetch ([0-9a-f]{40,64}) from promisor remote`)

func (s *Store) push(ctx context.Context, old, commit string) (bool, error) {
	ref := "refs/heads/" + s.Branch
	// over HTTP every push is thin whatever it is told: so is every push
	// here, whose bases Update fetched first
	args := []string{"push", "--thin", "--porcelain", "--no-verify"}

	switch {
	case s.Roots:
		args = append(args, "--force-with-lease="+ref+":"+old, remoteName, commit+":"+ref)
	default:
		// a commit on the tip read moves the branch forward only while it
		// still points there; the remote refuses it otherwise
		args = append(args, remoteName, commit+":"+ref)
	}

	out, err := s.git(ctx, []string{noLazyFetch}, nil, args...)
	if err == nil {
		if moved(out, ref) {
			return false, nil
		}

		err = fmt.Errorf("%w: the push said nothing of moving %s: %s", ErrGit, ref, strings.TrimSpace(out))
		ge := &gitError{args: args, err: err, stdout: out}

		for line := range strings.SplitSeq(out, "\n") {
			if lostRace(line) {
				return true, fmt.Errorf("%w: %s", ge, strings.TrimSpace(line))
			}
		}

		return false, err
	}

	ge, ok := errors.AsType[*gitError](err)
	if !ok {
		return false, err
	}

	if m := missingRE.FindStringSubmatch(ge.stderr); m != nil {
		return false, fmt.Errorf("%w: %w: the push needed %s, which its scratch repository never fetched: trying again repeats it: %w", ErrGit, ErrMissing, m[1], err)
	}

	var refused []string

	for line := range strings.SplitSeq(ge.stdout+"\n"+ge.stderr, "\n") {
		if lostRace(line) {
			return true, fmt.Errorf("%w: %s", err, strings.TrimSpace(line))
		}

		if strings.HasPrefix(line, "!") {
			refused = append(refused, strings.TrimSpace(line))
		}
	}

	// the remote's own words say why, beside git's
	if len(refused) > 0 {
		return false, fmt.Errorf("%w: %s", err, strings.Join(refused, "; "))
	}

	return false, err
}

func moved(out, ref string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		flag, rest, ok := strings.Cut(line, "\t")
		if ok && (flag == " " || flag == "+" || flag == "*") && strings.Contains(rest, ":"+ref+"\t") {
			return true
		}
	}

	return false
}

func lostRace(line string) bool {
	for _, why := range []string{
		"(fetch first)", "(non-fast-forward)", "(stale info)", "(already exists)",
		"but expected", "cannot lock ref", "incorrect old value", "reference already exists",
	} {
		if strings.Contains(line, why) {
			return true
		}
	}

	return false
}
