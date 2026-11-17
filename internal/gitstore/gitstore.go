// Package gitstore keeps small records for state that CI steps write while a
// run goes, each a compare-and-swap cell: a write names the value it expects
// and applies only then. Records live as refs of a remote, each pointing at
// a parentless commit (git's force-with-lease guards a write), or as
// directories of one branch (branch.go).
package gitstore

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
	"maps"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/internal/tries"
)

// Store is a remote repository's refs, read and written through a scratch
// repository of its own. A Store serves one goroutine at a time: each
// process makes its own, in a directory no other store shares.
type Store struct {
	// Remote is the repository's URL. It never carries a token.
	Remote string
	// Token, when set, authenticates to an HTTPS remote as a GitHub token.
	// It reaches git through the environment, never through its arguments,
	// a URL or a log.
	Token string
	Env   []string
	Dir   string
	Clock func() time.Time
	// Retries is how often a write that failed in passing goes again at
	// least (0: tried once); a refusal by a rule of the remote's goes again
	// only a few times, a defect never, and a write whose ref moved is never
	// retried here.
	Retries  int
	Backoff  time.Duration
	RetryFor time.Duration
	Until    time.Time

	// Seed, when set and Dir is not, is where a scratch repository waits
	// between jobs on one machine: a job takes it by renaming it, so two
	// jobs never share one, and puts it back at Close.
	Seed string

	Branch *branchstore.Store

	// Proposed marks a store used for a proposed change's run, or any run
	// that isn't trusted: its caller writes the run's own records and
	// nothing later plans bind with (answers, lists, the sweep's deletes).
	// A store kept apart for trusted records would refuse such a caller
	// its writes; one shared store relies on its callers asking this.
	Proposed bool

	Batch string

	// ReadOnly refuses every write: a step that runs a change's own code
	// reads the records and writes none.
	ReadOnly bool

	temp    bool
	seeded  bool
	g       grouped
	retried atomic.Int64
	b       branched
}

// Retried says how many retries the store's writes needed so far.
func (s *Store) Retried() int {
	if s.onBranch() {
		return s.Branch.Retried()
	}

	return int(s.retried.Load())
}

// ErrGit wraps a git command that failed.
var ErrGit = errors.New("git")

// ErrReadOnly is a write to a store opened read only.
var ErrReadOnly = errors.New("the store is read only here")

// ErrStale is returned when a ref no longer had the value a write expected:
// someone else wrote it first. Nothing of that write was applied.
var ErrStale = errors.New("a ref changed under the write")

const (
	quiet          = "--quiet"
	mirror         = "refs/mirror/"
	transportTries = 3
	author         = "pipelinegen"
	authorEmail    = "pipelinegen@users.noreply.github.com"
)

// Ref is a ref of the remote and the commit it points at.
type Ref struct {
	Name   string
	Commit string
	Time   time.Time
}

// Update writes or removes one ref.
type Update struct {
	Ref string
	// Old is the commit the ref must point at for the write to apply; empty,
	// the ref must not exist. Ignored with Any.
	Old string
	// Any applies the write whatever the ref holds: for a ref only one
	// writer ever writes, or a removal by hand.
	Any bool
	// Files are the record's content; a nil map with Delete removes the
	// ref.
	Files  map[string][]byte
	Delete bool
}

// Close removes the scratch repository when the store made it.
func (s *Store) Close() error {
	if s.onBranch() {
		return s.Branch.Close()
	}

	if s.Dir != "" {
		// the grouped refs' scratch repository lives for one job
		if err := os.RemoveAll(s.Dir + ".groups"); err != nil {
			return fmt.Errorf("remove scratch repository: %w", err)
		}
	}

	if !s.temp || s.Dir == "" {
		return nil
	}

	if s.seeded && s.putBack() {
		s.Dir, s.temp, s.seeded = "", false, false

		return nil
	}

	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("remove scratch repository: %w", err)
	}

	s.Dir, s.temp, s.seeded = "", false, false

	return nil
}

func (s *Store) init(ctx context.Context) error {
	if s.Remote == "" {
		return fmt.Errorf("%w: a store needs a remote", ErrGit)
	}

	if s.Dir == "" && s.Seed != "" {
		if err := s.claim(ctx); err != nil {
			return err
		}
	}

	if s.Dir == "" {
		dir, err := os.MkdirTemp("", "pipelinegen-store-")
		if err != nil {
			return fmt.Errorf("scratch repository: %w", err)
		}

		s.Dir, s.temp = dir, true
	}

	if _, err := os.Stat(filepath.Join(s.Dir, "HEAD")); err == nil {
		return nil
	}

	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	if _, err := s.git(ctx, nil, nil, "init", quiet, "--bare"); err != nil {
		return err
	}

	// a scratch repository lives for one job: a garbage collection that a
	// fetch starts in the background only costs time, and still runs
	// when the job removes the repository
	_, err := s.git(ctx, nil, nil, "config", "gc.auto", "0")

	return err
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

// redact keeps every secret, the token and its Basic form included, out of
// anything git prints.
func (s *Store) redact(text string) string {
	if s.Token != "" {
		secret.Add(s.Token)
	}

	return secret.Redact(text)
}

func (s *Store) policy() tries.Policy {
	return tries.Policy{Retries: s.Retries, First: s.Backoff, Deadline: s.RetryFor, Until: s.Until}
}

func kindOf(err error) tries.Kind {
	if errors.Is(err, ErrNoAtomic) || errors.Is(err, ErrMissing) {
		return tries.Defect
	}

	return tries.Of(err.Error())
}

func (s *Store) pause(ctx context.Context, try int) {
	t := time.NewTimer(time.Duration(try*try)*200*time.Millisecond + time.Duration(mrand.Int64N(int64(200*time.Millisecond)))) //nolint:gosec // jitter, not a secret

	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// List fetches every ref under the prefixes (each ending in "/") in one
// round trip and returns them by name.
func (s *Store) List(ctx context.Context, prefixes ...string) (map[string]Ref, error) {
	if s.onBranch() {
		return s.branchList(ctx, prefixes)
	}

	if err := s.init(ctx); err != nil {
		return nil, err
	}

	args := []string{gitFetch, quiet, noTags, "--prune", noFetchHead, s.Remote}
	patterns := make([]string, 0, len(prefixes))

	for _, p := range prefixes {
		if !strings.HasPrefix(p, "refs/") || !strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("%w: a prefix is refs/.../: %q", ErrGit, p)
		}

		local := mirror + strings.TrimPrefix(p, "refs/")
		args = append(args, "+"+p+"*:"+local+"*")
		patterns = append(patterns, local)
	}

	if _, err := s.retry(ctx, args); err != nil {
		return nil, err
	}

	refs, err := s.local(ctx, patterns)
	if err != nil {
		return nil, err
	}

	if err := s.groupedList(ctx, prefixes, refs); err != nil {
		return nil, err
	}

	return refs, nil
}

func (s *Store) local(ctx context.Context, patterns []string) (map[string]Ref, error) {
	out, err := s.git(ctx, nil, nil, append([]string{"for-each-ref", "--format=%(refname) %(objectname) %(committerdate:unix)"}, patterns...)...)
	if err != nil {
		return nil, err
	}

	refs := map[string]Ref{}

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}

		unix, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: commit time %q", ErrGit, fields[2])
		}

		name := "refs/" + strings.TrimPrefix(fields[0], mirror)
		refs[name] = Ref{Name: name, Commit: fields[1], Time: time.Unix(unix, 0).UTC()}
	}

	return refs, nil
}

// Files reads a record: every file of a commit that List or First
// returned.
func (s *Store) Files(ctx context.Context, commit string) (map[string][]byte, error) {
	return s.FilesBut(ctx, commit, "")
}

// FilesBut reads a record as Files does, but for what lies under dir,
// which it never fetches.
func (s *Store) FilesBut(ctx context.Context, commit, dir string) (map[string][]byte, error) {
	dir = strings.Trim(dir, "/")

	if s.onBranch() {
		if dir == "" {
			return s.branchRead(ctx, commit, nil)
		}

		p, v, err := s.record(commit)
		if err != nil {
			return nil, err
		}

		files, err := v.Files(ctx, p)
		if err != nil {
			return nil, err
		}

		paths := make([]string, 0, len(files))

		for _, f := range files {
			if rel := strings.TrimPrefix(f, p+"/"); !under(rel, dir) {
				paths = append(paths, rel)
			}
		}

		return s.branchRead(ctx, commit, paths)
	}

	out, err := s.git(ctx, nil, nil, "ls-tree", "-r", "-z", "--name-only", commit)
	if err != nil {
		return nil, err
	}

	var paths []string

	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" && (dir == "" || !under(p, dir)) {
			paths = append(paths, p)
		}
	}

	return s.read(ctx, commit, paths)
}

func under(p, dir string) bool { return dir != "" && (p == dir || strings.HasPrefix(p, dir+"/")) }

// Under reads the files of a record under one directory of it, by their
// paths in the record, and nothing else of it.
func (s *Store) Under(ctx context.Context, commit, dir string) (map[string][]byte, error) {
	dir = strings.Trim(dir, "/")

	var names []string

	if s.onBranch() {
		p, v, err := s.record(commit)
		if err != nil {
			return nil, err
		}

		files, err := v.Files(ctx, p+"/"+dir)
		if err != nil {
			return nil, err
		}

		if len(files) == 0 {
			// no paths would read the whole record
			return map[string][]byte{}, nil
		}

		for _, f := range files {
			names = append(names, strings.TrimPrefix(f, p+"/"))
		}

		return s.branchRead(ctx, commit, names)
	}

	out, err := s.git(ctx, nil, nil, "ls-tree", "-r", "-z", "--name-only", commit, "--", dir)
	if err != nil {
		return nil, err
	}

	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			names = append(names, p)
		}
	}

	return s.read(ctx, commit, names)
}

// File reads one file of a record; false when it has none at that path.
func (s *Store) File(ctx context.Context, commit, path string) ([]byte, bool, error) {
	read := s.read
	if s.onBranch() {
		read = s.branchRead
	}

	got, err := read(ctx, commit, []string{path})
	if err != nil {
		return nil, false, err
	}

	data, ok := got[path]

	return data, ok, nil
}

// FileEach reads the file at path in each of the commits, in one git
// process; commits without it are left out.
func (s *Store) FileEach(ctx context.Context, commits []string, path string) (map[string][]byte, error) {
	if s.onBranch() {
		out := make(map[string][]byte, len(commits))

		for _, c := range commits {
			got, err := s.branchRead(ctx, c, []string{path})
			if err != nil {
				return nil, err
			}

			if data, ok := got[path]; ok {
				out[c] = data
			}
		}

		return out, nil
	}

	keys := make([]string, 0, len(commits))
	for _, c := range commits {
		keys = append(keys, c+":"+path)
	}

	got, err := s.batch(ctx, keys)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(got))
	for _, c := range commits {
		if data, ok := got[c+":"+path]; ok {
			out[c] = data
		}
	}

	return out, nil
}

func (s *Store) read(ctx context.Context, commit string, paths []string) (map[string][]byte, error) {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, commit+":"+p)
	}

	got, err := s.batch(ctx, keys)
	if err != nil {
		return nil, err
	}

	files := make(map[string][]byte, len(got))
	for _, p := range paths {
		if data, ok := got[commit+":"+p]; ok {
			files[p] = data
		}
	}

	return files, nil
}

func (s *Store) batch(ctx context.Context, keys []string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if len(keys) == 0 {
		return files, nil
	}

	var in bytes.Buffer

	for _, k := range keys {
		if strings.Contains(k, "\n") {
			return nil, fmt.Errorf("%w: a path with a line break: %q", ErrGit, k)
		}

		in.WriteString(k + "\n")
	}

	out, err := s.git(ctx, nil, &in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	r := bufio.NewReader(strings.NewReader(out))

	for _, p := range keys {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%w: cat-file output ends early", ErrGit)
		}

		fields := strings.Fields(header)
		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}

		if len(fields) != 3 {
			return nil, fmt.Errorf("%w: cat-file says %q", ErrGit, strings.TrimSpace(header))
		}

		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("%w: cat-file size %q", ErrGit, fields[2])
		}

		data := make([]byte, size+1)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("%w: cat-file output ends early", ErrGit)
		}

		if fields[1] == "blob" {
			files[p] = data[:size]
		}
	}

	return files, nil
}

const firstBatch = 100

// First returns the first of the refs named, in their order, that exists
// on the remote, fetched so its files can be read: given a commit's ref
// and then its ancestors', the nearest one that has a record.
func (s *Store) First(ctx context.Context, refs []string) (Ref, bool, error) {
	if s.onBranch() {
		return s.branchFirst(ctx, refs)
	}

	// a run's record is in its own ref or among the run's refs
	for i, name := range refs {
		if _, ok := recordOf(name); !ok {
			continue
		}

		got, ok, err := s.First(ctx, refs[:i])
		if err != nil || ok {
			return got, ok, err
		}

		if got, ok, err = s.firstRef(ctx, []string{name}); err != nil || ok {
			return got, ok, err
		}

		if got, ok, err = s.groupedFirst(ctx, name); err != nil || ok {
			return got, ok, err
		}

		return s.First(ctx, refs[i+1:])
	}

	return s.firstRef(ctx, refs)
}

func (s *Store) firstRef(ctx context.Context, refs []string) (Ref, bool, error) {
	if err := s.init(ctx); err != nil {
		return Ref{}, false, err
	}

	for start := 0; start < len(refs); start += firstBatch {
		batch := refs[start:min(start+firstBatch, len(refs))]

		out, err := s.retry(ctx, append([]string{lsRemote, refsOnly, s.Remote}, batch...))
		if err != nil {
			return Ref{}, false, err
		}

		have := map[string]bool{}

		for line := range strings.SplitSeq(out, "\n") {
			if _, name, ok := strings.Cut(line, "\t"); ok {
				have[name] = true
			}
		}

		for _, ref := range batch {
			if !have[ref] {
				continue
			}

			got, err := s.fetchOne(ctx, ref)
			if err != nil {
				return Ref{}, false, err
			}

			return got, true, nil
		}
	}

	return Ref{}, false, nil
}

func (s *Store) fetchOne(ctx context.Context, ref string) (Ref, error) {
	if !strings.HasPrefix(ref, "refs/") {
		return Ref{}, fmt.Errorf("%w: bad ref name %q", ErrGit, ref)
	}

	local := mirror + strings.TrimPrefix(ref, "refs/")
	if _, err := s.retry(ctx, []string{gitFetch, quiet, noTags, noFetchHead, s.Remote, "+" + ref + ":" + local}); err != nil {
		return Ref{}, err
	}

	refs, err := s.local(ctx, []string{local})
	if err != nil {
		return Ref{}, err
	}

	got, ok := refs[ref]
	if !ok {
		return Ref{}, fmt.Errorf("%w: %s fetched but not found", ErrGit, ref)
	}

	return got, nil
}

func (s *Store) retry(ctx context.Context, args []string) (string, error) {
	var (
		out string
		err error
	)

	for try := range transportTries {
		if out, err = s.git(ctx, nil, nil, args...); err == nil || ctx.Err() != nil {
			break
		}

		s.pause(ctx, try+1)
	}

	return out, err
}

// Write applies the updates in one atomic push: all of them or, when any
// ref no longer holds the value its update expects, none (ErrStale). It
// returns the new commit of every ref written.
func (s *Store) Write(ctx context.Context, message string, updates ...Update) (map[string]string, error) {
	if s.ReadOnly {
		return nil, fmt.Errorf("%w: %s", ErrReadOnly, message)
	}

	if s.onBranch() {
		return s.branchWrite(ctx, message, updates)
	}

	var grouped, plain []Update

	for _, u := range updates {
		r, ok := recordOf(u.Ref)
		if !ok {
			plain = append(plain, u)

			continue
		}

		old, err := s.layoutOf(ctx, u, r)
		if err != nil {
			return nil, err
		}

		if old {
			plain = append(plain, u)
		} else {
			grouped = append(grouped, u)
		}
	}

	switch {
	case len(grouped) == 0:
		return s.refsWrite(ctx, message, updates)
	case len(plain) == 0:
		return s.groupedWrite(ctx, message, grouped)
	}

	// only a sweep meets a run in both layouts, deleting what it listed
	for _, u := range plain {
		if _, ok := recordOf(u.Ref); !ok || !u.Delete {
			return nil, fmt.Errorf("%w: %s", ErrMixed, u.Ref)
		}
	}

	out, err := s.refsWrite(ctx, message, plain)
	if err != nil {
		return nil, err
	}

	more, err := s.groupedWrite(ctx, message, grouped)
	maps.Copy(out, more)

	return out, err
}

func (s *Store) layoutOf(ctx context.Context, u Update, r record) (bool, error) {
	if u.Delete {
		s.g.mu.Lock()
		_, grouped := s.g.where[u.Ref]
		s.g.mu.Unlock()

		return !grouped, nil
	}

	return s.oneRefEach(ctx, r.run)
}

func (s *Store) refsWrite(ctx context.Context, message string, updates []Update) (map[string]string, error) {
	if err := s.init(ctx); err != nil {
		return nil, err
	}

	commits := map[string]string{}
	args := []string{gitPush, porcelain, quiet, atomicPush, s.Remote}

	for _, u := range updates {
		if err := s.checkRef(ctx, u.Ref); err != nil {
			return nil, err
		}

		src := ""

		if !u.Delete {
			c, err := s.commit(ctx, message, u.Files)
			if err != nil {
				return nil, err
			}

			commits[u.Ref], src = c, c
		}

		// a "+" on the refspec would override the lease, so a guarded
		// write never has one
		switch {
		case u.Any && u.Delete:
			args = append(args, ":"+u.Ref)
		case u.Any:
			args = append(args, "+"+src+":"+u.Ref)
		default:
			args = append(args, "--force-with-lease="+u.Ref+":"+u.Old, src+":"+u.Ref)
		}
	}

	if len(updates) == 0 {
		return commits, nil
	}

	var err error

	// every new commit is unique, so a try after a push that did land is
	// "up to date" and succeeds rather than failing its lease; a push the
	// remote refused for a reason of its own applied none of its refs, so
	// what is still missing is the whole write, and it goes again
	run := s.policy().Start()

	for {
		var stale bool

		stale, err = s.push(ctx, args)
		if stale {
			return nil, fmt.Errorf("%w: %w", ErrStale, err)
		}

		if err == nil || !run.Again(ctx, kindOf(err)) {
			break
		}

		s.retried.Add(1)
	}

	err = run.Err(err)

	// a write guarded by what it read is answered by reading again, which
	// is as right after a refusal as after a lost race: never data lost
	if errors.Is(err, errRefused) && guarded(updates) {
		return nil, fmt.Errorf("%w: %w", ErrStale, err)
	}

	if err != nil {
		return nil, err
	}

	return commits, nil
}

func (s *Store) checkRef(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("%w: bad ref name %q", ErrGit, ref)
	}

	if _, err := s.git(ctx, nil, nil, "check-ref-format", ref); err != nil {
		return fmt.Errorf("%w: bad ref name %q", ErrGit, ref)
	}

	return nil
}

func (s *Store) push(ctx context.Context, args []string) (stale bool, err error) {
	_, err = s.git(ctx, nil, nil, args...)
	if err == nil {
		return false, nil
	}

	if ge, ok := errors.AsType[*gitError](err); ok {
		if strings.Contains(ge.stderr, noAtomicPush) {
			return false, fmt.Errorf("%w: %w", ErrGit, ErrNoAtomic)
		}

		var refused []string

		for line := range strings.SplitSeq(ge.stdout, "\n") {
			if !strings.HasPrefix(line, "!") || strings.Contains(line, "atomic push failed") {
				continue
			}

			if leaseLost(line) {
				return true, errors.New(strings.TrimSpace(line)) //nolint:err113 // git's own words, under ErrStale
			}

			refused = append(refused, strings.TrimSpace(line))
		}

		if len(refused) > 0 {
			return false, fmt.Errorf("%w: %w: %s%s", ErrGit, errRefused, strings.Join(refused, "; "), remoteSaid(ge.stdout+"\n"+ge.stderr))
		}
	}

	return false, err
}

var errRefused = errors.New("the remote refused the push")

// ErrNoAtomic is a remote that takes no atomic pushes: a write of several
// refs could land in part, so the records can't be kept in refs there.
// Trying again changes nothing.
var ErrNoAtomic = errors.New("the remote takes no atomic pushes, which keeping the records in refs needs: set store.backend: branch")

const noAtomicPush = "does not support --atomic push"

func remoteSaid(stderr string) string {
	var said []string

	for line := range strings.SplitSeq(stderr, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "remote:"); ok && strings.TrimSpace(rest) != "" {
			said = append(said, strings.TrimSpace(rest))
		}
	}

	if len(said) == 0 {
		return refusedHint(stderr)
	}

	return " (the remote said: " + strings.Join(said, "; ") + ")" + refusedHint(stderr)
}

// refusedHint is what to do about a remote that declined pipelinegen's refs
// by a rule of its own; a busy remote declines the same way, so the refusal
// is retried all the same.
func refusedHint(stderr string) string {
	for _, why := range []string{"hook declined", "hidden ref", "denied", "not allowed"} {
		if strings.Contains(stderr, why) {
			return "; a remote that takes only branches and tags keeps the records with store.backend: branch"
		}
	}

	return ""
}

func guarded(updates []Update) bool {
	for _, u := range updates {
		if u.Any {
			return false
		}
	}

	return true
}

func leaseLost(line string) bool {
	for _, why := range []string{"(stale info)", "(fetch first)", "(already exists)", "(non-fast-forward)", "but expected"} {
		if strings.Contains(line, why) {
			return true
		}
	}

	return false
}

// commit makes a parentless commit holding files; a nonce in its message
// makes it unique, since a push of the value a ref already holds would pass
// whatever its lease.
func (s *Store) commit(ctx context.Context, message string, files map[string][]byte) (string, error) {
	index, err := s.tempIndex()
	if err != nil {
		return "", err
	}

	defer os.Remove(index) //nolint:errcheck // a leftover index in the scratch repository harms nothing

	env := []string{"GIT_INDEX_FILE=" + index}

	info, err := s.blobs(ctx, files)
	if err != nil {
		return "", err
	}

	if _, err := s.git(ctx, env, info, "update-index", "--add", "--index-info"); err != nil {
		return "", err
	}

	tree, err := s.git(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}

	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	var dated []string

	if s.Clock != nil {
		date := strconv.FormatInt(s.Clock().Unix(), 10) + " +0000"
		dated = []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	}

	out, err := s.git(ctx, dated, nil, "commit-tree", strings.TrimSpace(tree), "-m", message, "-m", "nonce "+hex.EncodeToString(nonce))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

func (s *Store) blobs(ctx context.Context, files map[string][]byte) (*bytes.Buffer, error) {
	names := slices.Sorted(maps.Keys(files))

	dir, err := os.MkdirTemp(s.Dir, "blobs-")
	if err != nil {
		return nil, fmt.Errorf("blobs: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover in the scratch repository harms nothing

	var list bytes.Buffer

	for i, p := range names {
		if p == "" || strings.ContainsAny(p, "\n\t") || strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("%w: bad path %q", ErrGit, p)
		}

		f := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(f, files[p], 0o600); err != nil {
			return nil, fmt.Errorf("blobs: %w", err)
		}

		list.WriteString(f + "\n")
	}

	var info bytes.Buffer
	if len(names) == 0 {
		return &info, nil
	}

	out, err := s.git(ctx, nil, &list, "hash-object", "-w", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}

	ids := strings.Fields(out)
	if len(ids) != len(names) {
		return nil, fmt.Errorf("%w: hash-object gave %d ids for %d files", ErrGit, len(ids), len(names))
	}

	for i, p := range names {
		fmt.Fprintf(&info, "100644 %s\t%s\n", ids[i], p)
	}

	return &info, nil
}

// tempIndex names a fresh index file in the scratch repository, so that
// commits made at once never share one.
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
