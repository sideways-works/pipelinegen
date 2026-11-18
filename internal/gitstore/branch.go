package gitstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// A branch store keeps each record as a directory <p> (for refs/pipelinegen/<p>) beside an empty
// marker <p>:<unix>.<nonce>, new with every write, so a write guarded by what it read loses to one between.

const root = "refs/pipelinegen/"

const marks = ":"

type branched struct {
	mu     sync.Mutex
	tokens map[string]string // token -> record path
	view   *branchstore.View
}

func (s *Store) onBranch() bool { return s.Branch != nil }

func dirOf(ref string) (string, error) {
	p, ok := strings.CutPrefix(ref, root)
	if !ok || p == "" || strings.Contains(p, marks) {
		return "", fmt.Errorf("%w: a branch store holds refs under %s only: %q", ErrGit, root, ref)
	}

	return strings.TrimSuffix(p, "/"), nil
}

func newToken(now time.Time) (string, error) {
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	return strconv.FormatInt(now.Unix(), 10) + "." + hex.EncodeToString(nonce), nil
}

func tokenTime(tok string) time.Time {
	unix, nonce, ok := strings.Cut(tok, ".")
	if !ok || len(nonce) != 12 || strings.Trim(nonce, "0123456789abcdef") != "" {
		return time.Time{}
	}

	n, err := strconv.ParseInt(unix, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}

	return time.Unix(n, 0).UTC()
}

func (s *Store) clock() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}

	return time.Now()
}

func (s *Store) snapshot(ctx context.Context) (*branchstore.View, error) {
	v, err := s.Branch.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	s.b.mu.Lock()
	s.b.view = v
	s.b.mu.Unlock()

	return v, nil
}

func (s *Store) markers(ctx context.Context, v *branchstore.View, dir string) (map[string]Ref, error) {
	files, err := v.Files(ctx, dir)
	if err != nil {
		return nil, err
	}

	refs := map[string]Ref{}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	if s.b.tokens == nil {
		s.b.tokens = map[string]string{}
	}

	for _, f := range files {
		s.remember(refs, path.Dir(f), path.Base(f))
	}

	return refs, nil
}

// remember adds the record whose marker is base to refs; a record's own files may hold the marker
// character, so only a name whose part after it is a token counts. The caller holds s.b.mu.
func (s *Store) remember(refs map[string]Ref, dir, base string) {
	rec, tok, ok := strings.Cut(base, marks)
	if !ok || tokenTime(tok).IsZero() {
		return
	}

	p := path.Join(dir, rec)
	name := root + p
	refs[name] = Ref{Name: name, Commit: tok, Time: tokenTime(tok)}
	s.b.tokens[tok] = p
}

func (s *Store) branchList(ctx context.Context, prefixes []string) (map[string]Ref, error) {
	v, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]Ref{}

	for _, p := range prefixes {
		if !strings.HasPrefix(p, root) || !strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("%w: a prefix is %s.../: %q", ErrGit, root, p)
		}

		got, err := s.markers(ctx, v, strings.TrimSuffix(strings.TrimPrefix(p, root), "/"))
		if err != nil {
			return nil, err
		}

		maps.Copy(out, got)
	}

	return out, nil
}

func (s *Store) branchFirst(ctx context.Context, refs []string) (Ref, bool, error) {
	v, err := s.snapshot(ctx)
	if err != nil {
		return Ref{}, false, err
	}

	byParent := map[string]map[string]Ref{}

	for _, ref := range refs {
		p, err := dirOf(ref)
		if err != nil {
			return Ref{}, false, err
		}

		parent := path.Dir(p)
		if parent == "." {
			parent = ""
		}

		got, ok := byParent[parent]
		if !ok {
			if got, err = s.siblings(ctx, v, parent); err != nil {
				return Ref{}, false, err
			}

			byParent[parent] = got
		}

		if r, ok := got[ref]; ok {
			return r, true, nil
		}
	}

	return Ref{}, false, nil
}

func (s *Store) siblings(ctx context.Context, v *branchstore.View, dir string) (map[string]Ref, error) {
	names, err := v.List(ctx, dir)
	if err != nil {
		return nil, err
	}

	refs := map[string]Ref{}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	if s.b.tokens == nil {
		s.b.tokens = map[string]string{}
	}

	for _, base := range names {
		s.remember(refs, dir, base)
	}

	return refs, nil
}

func (s *Store) record(tok string) (string, *branchstore.View, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	p, ok := s.b.tokens[tok]
	if !ok || s.b.view == nil {
		return "", nil, fmt.Errorf("%w: no record %q was listed", ErrGit, tok)
	}

	return p, s.b.view, nil
}

func (s *Store) branchRead(ctx context.Context, tok string, paths []string) (map[string][]byte, error) {
	p, v, err := s.record(tok)
	if err != nil {
		return nil, err
	}

	if paths == nil {
		files, err := v.Files(ctx, p)
		if err != nil {
			return nil, err
		}

		paths = make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, strings.TrimPrefix(f, p+"/"))
		}
	}

	full := make([]string, 0, len(paths))
	for _, f := range paths {
		full = append(full, p+"/"+f)
	}

	got, err := v.Read(ctx, full)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(got))
	for f, data := range got {
		out[strings.TrimPrefix(f, p+"/")] = data
	}

	return out, nil
}

func (s *Store) apply(ctx context.Context, v *branchstore.View, u Update, p, tok string, c *branchstore.Change) error {
	parent := path.Dir(p)
	if parent == "." {
		parent = ""
	}

	now, err := s.siblings(ctx, v, parent)
	if err != nil {
		return err
	}

	cur := now[u.Ref].Commit

	// this very write landed already, its answer lost: done
	if cur == tok {
		return nil
	}

	if !u.Any && cur != u.Old {
		return errStaleRecord
	}

	c.Deletes = append(c.Deletes, p)
	if cur != "" {
		c.Deletes = append(c.Deletes, p+marks+cur)
	}

	if u.Delete {
		return nil
	}

	c.Files[p+marks+tok] = nil
	for f, data := range u.Files {
		c.Files[p+"/"+f] = data
	}

	return nil
}

func refNames(updates []Update) string {
	names := make([]string, 0, len(updates))
	for _, u := range updates {
		names = append(names, u.Ref)
	}

	return strings.Join(names, ", ")
}

// checkRefName refuses a name git wouldn't take for a ref, so a branch store
// holds the same names as one on refs.
func checkRefName(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("%w: bad ref name %q", ErrGit, ref)
	}

	cmd := exec.CommandContext(ctx, "git", "check-ref-format", ref) //nolint:gosec // git checks a name, nothing more
	cmd.Env = secret.HardenGit(os.Environ())

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: bad ref name %q", ErrGit, ref)
	}

	return nil
}

var errStaleRecord = errors.New("stale")

func (s *Store) branchWrite(ctx context.Context, message string, updates []Update) (map[string]string, error) {
	dirs := make([]string, len(updates))
	toks := make([]string, len(updates))

	for i, u := range updates {
		if err := checkRefName(ctx, u.Ref); err != nil {
			return nil, err
		}

		var err error

		if dirs[i], err = dirOf(u.Ref); err != nil {
			return nil, err
		}

		// each record its own token, so a token names one record
		if toks[i], err = newToken(s.clock()); err != nil {
			return nil, err
		}
	}

	// the store's own retries and backoff govern its writes
	s.Branch.Tries(s.policy())

	err := s.Branch.Update(ctx, message, func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		c := branchstore.Change{Files: map[string][]byte{}}

		for i, u := range updates {
			if err := s.apply(ctx, v, u, dirs[i], toks[i], &c); err != nil {
				return c, err
			}
		}

		return c, nil
	})

	switch {
	case errors.Is(err, errStaleRecord):
		return nil, fmt.Errorf("%w: a record changed under the write", ErrStale)
	case err != nil && ctx.Err() == nil && guarded(updates):
		// as on refs: a write guarded by what it read is answered by reading again, as right after
		// a refusal as after a lost race
		return nil, fmt.Errorf("%w: %s: %w", ErrStale, refNames(updates), err)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", refNames(updates), err)
	}

	out := map[string]string{}

	for i, u := range updates {
		if !u.Delete {
			out[u.Ref] = toks[i]
		}
	}

	return out, nil
}
