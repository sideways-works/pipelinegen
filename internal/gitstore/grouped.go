package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	mrand "math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/tries"
)

// A run's records live in few refs, as every push is told of every ref: each batch writes a ref of its own,
// moved into the run's one ref at the suite's end (Compact); each record is a directory with a marker beside.

const (
	gitFetch    = "fetch"
	noTags      = "--no-tags"
	noFetchHead = "--no-write-fetch-head"
	prune       = "--prune"
	lsRemote    = "ls-remote"
	refsOnly    = "--refs"
	gitPush     = "push"
	porcelain   = "--porcelain"
	atomicPush  = "--atomic"
	promisor    = "store"
)

const (
	runsRoot  = "refs/pipelinegen/runs/"
	plansRoot = "refs/pipelinegen/plans/"
	// RunPrefix holds one ref per run: refs/pipelinegen/run/<run>/<change>/<c12>.
	RunPrefix = "refs/pipelinegen/run/"
	// BatchPrefix holds a live batch's records:
	// refs/pipelinegen/batch/<run>/<change>/<c12>/<suite>/<batch>.
	BatchPrefix = "refs/pipelinegen/batch/"
	planDir     = "_plan"
	groups      = "refs/groups/"
)

type record struct {
	run, dir string
}

func recordOf(name string) (record, bool) {
	if rest, ok := strings.CutPrefix(name, runsRoot); ok {
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) == 4 && parts[3] != "" {
			return record{run: strings.Join(parts[:3], "/"), dir: parts[3]}, true
		}

		return record{}, false
	}

	if rest, ok := strings.CutPrefix(name, plansRoot); ok {
		if parts := strings.Split(rest, "/"); len(parts) == 3 && !slices.Contains(parts, "") {
			return record{run: rest, dir: planDir}, true
		}
	}

	return record{}, false
}

func (r record) name() string {
	if r.dir == planDir {
		return plansRoot + r.run
	}

	return runsRoot + r.run + "/" + r.dir
}

func (r record) suite() string {
	first, rest, _ := strings.Cut(r.dir, "/")
	if first == "_end" {
		return rest
	}

	return first
}

func (r record) isState() bool {
	return r.dir != planDir && !strings.HasPrefix(r.dir, "_end/")
}

func runsAsked(prefix string) (string, bool) {
	// a prefix above the runs' records covers every run
	if strings.HasPrefix(runsRoot, prefix) || strings.HasPrefix(plansRoot, prefix) {
		return "", true
	}

	for _, root := range []string{runsRoot, plansRoot} {
		if rest, ok := strings.CutPrefix(prefix, root); ok {
			parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
			if rest == "" {
				return "", true
			}

			// a prefix inside one run's records reads that run whole and
			// keeps what it asked for
			return strings.Join(parts[:min(len(parts), 3)], "/"), true
		}
	}

	return "", false
}

type grouped struct {
	write   sync.Mutex
	mu      sync.Mutex
	where   map[string]groupRef
	layouts map[string]bool
}

type groupRef struct {
	ref, commit string
}

// groupedSpecs are the refspecs that fetch the grouped refs holding the prefixes' records, and the local
// prefixes they land under.
func groupedSpecs(prefixes []string) (specs, locals []string) {
	for _, p := range groupPatterns(prefixes) {
		local := groups + strings.TrimPrefix(p, "refs/")
		specs = append(specs, "+"+p+":"+local)
		locals = append(locals, strings.TrimSuffix(local, "*"))
	}

	return specs, locals
}

// groupedRead adds to refs the records the fetched grouped refs under locals hold within the prefixes.
func (s *Store) groupedRead(ctx context.Context, prefixes, locals []string, refs map[string]Ref) error {
	// the grouped refs themselves are the store's own business
	for name := range refs {
		if strings.HasPrefix(name, RunPrefix) || strings.HasPrefix(name, BatchPrefix) {
			delete(refs, name)
		}
	}

	if len(locals) == 0 {
		return nil
	}

	out, err := s.git(ctx, nil, nil, append([]string{"for-each-ref", "--format=%(refname) %(objectname)"}, locals...)...)
	if err != nil {
		return err
	}

	found := map[string]Ref{}

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		local, commit, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		ref := "refs/" + strings.TrimPrefix(local, groups)
		if err := s.recordsIn(ctx, ref, commit, found); err != nil {
			return err
		}
	}

	keepAsked(found, prefixes, refs)

	return nil
}

func groupPatterns(prefixes []string) []string {
	var patterns []string

	for _, p := range prefixes {
		run, ok := runsAsked(p)
		if !ok {
			continue
		}

		// every run of a change: a glob is whole parts
		parts := strings.Split(run, "/")
		change := strings.Join(parts[:min(len(parts), 2)], "/")

		for _, root := range []string{RunPrefix, BatchPrefix} {
			if run == "" {
				patterns = append(patterns, root+"*")
			} else {
				patterns = append(patterns, root+change+"/*")
			}
		}
	}

	slices.Sort(patterns)

	return slices.Compact(patterns)
}

func keepAsked(found map[string]Ref, prefixes []string, refs map[string]Ref) {
	for name, r := range found {
		if !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}

		if old, ok := refs[name]; !ok || r.Time.After(old.Time) {
			refs[name] = r
		}
	}
}

func (s *Store) recordsIn(ctx context.Context, ref, commit string, found map[string]Ref) error {
	run, ok := runOfGroup(ref)
	if !ok {
		return nil
	}

	out, err := s.git(ctx, nil, nil, "ls-tree", "-r", "-t", "-z", commit)
	if err != nil {
		return err
	}

	trees := map[string]string{}
	written := map[string]time.Time{}

	for line := range strings.SplitSeq(out, "\x00") {
		meta, p, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)

		if !ok || len(f) != 3 {
			continue
		}

		switch f[1] {
		case "tree":
			trees[p] = f[2]
		case blobKind:
			if dir, tok, ok := strings.Cut(path.Base(p), marks); ok {
				if when := tokenTime(tok); !when.IsZero() {
					written[path.Join(path.Dir(p), dir)] = when
				}
			}
		}
	}

	s.g.mu.Lock()
	defer s.g.mu.Unlock()

	if s.g.where == nil {
		s.g.where = map[string]groupRef{}
	}

	for dir, when := range written {
		id, ok := trees[dir]
		if !ok {
			continue
		}

		name := record{run: run, dir: dir}.name()
		if old, ok := found[name]; ok && !when.After(old.Time) {
			continue
		}

		found[name] = Ref{Name: name, Commit: id, Time: when}
		s.g.where[name] = groupRef{ref: ref, commit: commit}
	}

	return nil
}

const blobKind = "blob"

func runOfGroup(ref string) (string, bool) {
	if rest, ok := strings.CutPrefix(ref, RunPrefix); ok && strings.Count(rest, "/") == 2 {
		return rest, true
	}

	if rest, ok := strings.CutPrefix(ref, BatchPrefix); ok {
		if parts := strings.Split(rest, "/"); len(parts) == 5 {
			return strings.Join(parts[:3], "/"), true
		}
	}

	return "", false
}

// oneRefEach tells whether a run keeps one ref per record, being planned
// before the grouped layout; a run is never written in both layouts.
func (s *Store) oneRefEach(ctx context.Context, run string) (bool, error) {
	s.g.mu.Lock()
	old, ok := s.g.layouts[run]
	s.g.mu.Unlock()

	if ok {
		return old, nil
	}

	if err := s.init(ctx); err != nil {
		return false, err
	}

	out, err := s.retry(ctx, []string{lsRemote, refsOnly, s.Remote, plansRoot + run})
	if err != nil {
		return false, err
	}

	old = strings.Contains(out, "\t"+plansRoot+run+"\n") || strings.HasSuffix(strings.TrimSpace(out), "\t"+plansRoot+run)

	s.g.mu.Lock()
	defer s.g.mu.Unlock()

	if s.g.layouts == nil {
		s.g.layouts = map[string]bool{}
	}

	s.g.layouts[run] = old

	return old, nil
}

// ErrMixed is a write of a run's records together with other refs: the
// two are written apart.
var ErrMixed = errors.New("a write mixes a run's records with other refs")

// groupedWrite writes a slice's state into its batch's ref when the store writes for a batch, the rest
// into the run's ref; after a lost race it applies itself again, and a changed guarded record is ErrStale.
func (s *Store) groupedWrite(ctx context.Context, message string, updates []Update) (map[string]string, error) {
	// writes of one store share its scratch repository for the grouped
	// refs, whose fetches and indexes must not interleave
	s.g.write.Lock()
	defer s.g.write.Unlock()

	edits := map[string][]groupEdit{}

	for _, u := range updates {
		r, _ := recordOf(u.Ref)

		target := RunPrefix + r.run
		if u.Delete {
			s.g.mu.Lock()
			w, ok := s.g.where[u.Ref]
			s.g.mu.Unlock()

			if !ok {
				if u.Any {
					continue
				}

				return nil, fmt.Errorf("%w: %s was not listed", ErrStale, u.Ref)
			}

			target = w.ref
		} else if s.Batch != "" && r.isState() {
			target = BatchPrefix + r.run + "/" + r.suite() + "/" + s.Batch
		}

		tok, err := newToken(s.now())
		if err != nil {
			return nil, err
		}

		edits[target] = append(edits[target], groupEdit{u: u, rec: r, tok: tok})
	}

	if len(edits) == 0 {
		return map[string]string{}, nil
	}

	var out map[string]string

	err := s.again(ctx, "write", func() error {
		var err error

		out, err = s.groupedTry(ctx, message, edits)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (s *Store) again(ctx context.Context, what string, try func() error) error {
	deadline, bound := time.Now().Add(groupDeadline), "for "+groupDeadline.String()
	if end := tries.HardEnd(ctx, s.Until); !end.IsZero() && end.Before(deadline) {
		deadline, bound = end, "until the step's time limit was near"
	}

	run := s.policy().Start()

	for lost := 1; ; lost++ {
		err := try()

		switch {
		case err == nil:
			return nil
		case errors.Is(err, errLostRace):
			if time.Now().After(deadline) {
				return fmt.Errorf("%w: the run's refs kept moving under the %s %s", ErrGit, what, bound)
			}

			s.wait(ctx, lost)

			if ctx.Err() != nil {
				return fmt.Errorf("%w: %w", ErrGit, ctx.Err())
			}
		case errors.Is(err, errRefused) || errors.Is(err, errTransport) || errors.Is(err, ErrMissing):
			if !run.Again(ctx, kindOf(err)) {
				return run.Err(err)
			}

			s.retried.Add(1)
		default:
			return err
		}
	}
}

var errTransport = errors.New("reading the remote's refs failed")

// groupDeadline bounds how long a grouped write or read keeps trying, unless the step ends sooner.
const groupDeadline = 10 * time.Minute

var errLostRace = errors.New("lost race")

type groupEdit struct {
	u   Update
	rec record
	tok string
}

func (s *Store) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}

	return time.Now()
}

// wait waits a random time up to a bound that doubles with every lost
// race, so writers that collided spread out.
func (s *Store) wait(ctx context.Context, lost int) {
	bound := 100 * time.Millisecond << min(lost-1, 6)

	t := time.NewTimer(time.Duration(mrand.Int64N(int64(bound)))) //nolint:gosec // jitter, not a secret

	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Store) groupedTry(ctx context.Context, message string, edits map[string][]groupEdit) (map[string]string, error) {
	g, err := s.partial(ctx)
	if err != nil {
		return nil, err
	}

	targets := slices.Sorted(maps.Keys(edits))

	tips, err := g.fetch(ctx, targets)
	if err != nil {
		return nil, err
	}

	args := []string{gitPush, porcelain, quiet, atomicPush, s.Remote}
	out := map[string]string{}

	var bases []string

	for _, ref := range targets {
		commit, replaced, err := g.applyGroup(ctx, message, tips[ref], edits[ref])
		if err != nil {
			return nil, err
		}

		bases = append(bases, replaced...)

		switch commit {
		case tips[ref]:
			continue
		case "":
			// the ref's last record went
			args = append(args, "--force-with-lease="+ref+":"+tips[ref], ":"+ref)
		default:
			args = append(args, "--force-with-lease="+ref+":"+tips[ref], commit+":"+ref)
		}

		for _, e := range edits[ref] {
			if !e.u.Delete {
				out[e.u.Ref] = e.tok
			}
		}
	}

	if len(args) == 5 {
		return out, nil
	}

	// over HTTP git pushes a thin pack whatever it is told, reading each file the push replaces as
	// its delta base: those few files are fetched first, and nothing else ever is
	if err := g.fetchMissing(ctx, bases); err != nil {
		return nil, err
	}

	if err := g.pushGroups(ctx, args); err != nil {
		return nil, err
	}

	return out, nil
}

// fetchMissing fetches, at once, those of ids the partial repository lacks, known from the objects it
// holds, never by asking git of a missing one: a git before 2.45 dies on that with lazy fetching off.
func (s *Store) fetchMissing(ctx context.Context, ids []string) error {
	want, err := s.lacking(ctx, ids)
	if err != nil || len(want) == 0 {
		return err
	}

	args := []string{"-c", "fetch.negotiationAlgorithm=noop", gitFetch, quiet, noTags, noFetchHead, "--recurse-submodules=no", "--stdin", promisor}

	for try := range transportTries {
		if _, err = s.git(ctx, nil, strings.NewReader(strings.Join(want, "\n")+"\n"), args...); err == nil || ctx.Err() != nil {
			break
		}

		s.pause(ctx, try+1)
	}

	if err != nil {
		return fmt.Errorf("%w: the files the push replaces: %w", errTransport, err)
	}

	still, err := s.lacking(ctx, want)
	if err != nil {
		return err
	}

	if len(still) > 0 {
		return fmt.Errorf("%w: the remote gave none of %s, which the push replaces", errTransport, strings.Join(still, ", "))
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

func (s *Store) pushGroups(ctx context.Context, args []string) error {
	// a thin pack makes new files deltas against the ones they replace, fetched first; git pushes
	// thin packs over HTTP whatever it is told, so every remote is asked for them alike
	args = append([]string{args[0], "--thin"}, args[1:]...)

	_, err := s.git(ctx, []string{noLazyFetch}, nil, args...)
	if err == nil {
		return nil
	}

	ge, ok := errors.AsType[*gitError](err)
	if !ok {
		return err
	}

	if strings.Contains(ge.stderr, noAtomicPush) {
		return fmt.Errorf("%w: %w", ErrGit, ErrNoAtomic)
	}

	if m := missingRE.FindStringSubmatch(ge.stderr); m != nil {
		return fmt.Errorf("%w: %w: the push needed %s, which its scratch repository never fetched: it reads the remote's trees and only the files its writes replace, so trying again repeats it: %w", ErrGit, ErrMissing, m[1], err)
	}

	// git's own check against what the remote advertised, or the remote's
	// when the ref moved in between
	for line := range strings.SplitSeq(ge.stdout+"\n"+ge.stderr, "\n") {
		if leaseLost(line) || strings.Contains(line, "cannot lock ref") {
			return errLostRace
		}
	}

	return fmt.Errorf("%w: %w%s", errRefused, err, refusedHint(ge.stdout+ge.stderr))
}

// ErrMissing is a push that needed an object the store did not make and
// never fetched: a defect of pipelinegen, never retried.
var ErrMissing = errors.New("a defect of pipelinegen, please report it: a push needed an object the store never had")

var missingRE = regexp.MustCompile(`could not fetch ([0-9a-f]{40,64}) from promisor remote`)

// partial is the store's second scratch repository, a partial clone that reads the grouped refs' trees
// and never their files, so a write on a run's ref downloads nothing of what the run recorded.
func (s *Store) partial(ctx context.Context) (*Store, error) {
	if err := s.init(ctx); err != nil {
		return nil, err
	}

	// the same remote, reached the same way: its token, or its ssh command
	// with the store's key
	g := &Store{Remote: s.Remote, Token: s.Token, Env: s.Env, Dir: s.Dir + ".groups", Clock: s.Clock}
	if _, err := os.Stat(filepath.Join(g.Dir, "HEAD")); err == nil {
		return g, nil
	}

	if err := os.MkdirAll(g.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("scratch repository: %w", err)
	}

	const config = "config"

	for _, args := range [][]string{
		{"init", quiet, "--bare"},
		{"remote", "add", promisor, s.Remote},
		{config, "remote." + promisor + ".promisor", "true"},
		{config, "remote." + promisor + ".partialclonefilter", "blob:none"},
		{config, "extensions.partialClone", promisor},
		{config, "gc.auto", "0"},
		// git's sparse walk proves a file the remote holds only path by path, and would send one it
		// has at another path, which this repository never fetched; the whole walk is cheap here
		{config, "pack.useSparse", "false"},
	} {
		if _, err := g.git(ctx, nil, nil, args...); err != nil {
			return nil, err
		}
	}

	return g, nil
}

const noLazyFetch = "GIT_NO_LAZY_FETCH=1"

func (s *Store) fetch(ctx context.Context, refs []string) (map[string]string, error) {
	out, err := s.retry(ctx, append([]string{lsRemote, refsOnly, s.Remote}, refs...))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransport, err)
	}

	tips := map[string]string{}
	args := []string{gitFetch, quiet, noTags, noFetchHead, "--filter=blob:none", promisor}

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		commit, ref, ok := strings.Cut(line, "\t")
		if ok && slices.Contains(refs, ref) {
			tips[ref] = commit
			args = append(args, "+"+ref+":"+groups+strings.TrimPrefix(ref, "refs/"))
		}
	}

	if len(tips) == 0 {
		return tips, nil
	}

	if _, err := s.retry(ctx, args); err != nil {
		return nil, fmt.Errorf("%w: %w", errTransport, err)
	}

	// what the fetch brought, newer than what ls-remote said when a ref moved in between: a commit
	// without a parent brings nothing of the one before it
	for ref := range tips {
		out, err := s.git(ctx, nil, nil, "rev-parse", "--verify", groups+strings.TrimPrefix(ref, "refs/"))
		if err != nil {
			return nil, err
		}

		tips[ref] = strings.TrimSpace(out)
	}

	return tips, nil
}

func (s *Store) applyGroup(ctx context.Context, message, tip string, edits []groupEdit) (string, []string, error) {
	index, err := s.tempIndex()
	if err != nil {
		return "", nil, err
	}

	defer os.Remove(index) //nolint:errcheck // a leftover index in the scratch repository harms nothing

	env := []string{"GIT_INDEX_FILE=" + index, noLazyFetch}

	have := map[string]string{} // path -> blob id

	if tip != "" {
		if _, err := s.git(ctx, env, nil, "read-tree", tip); err != nil {
			return "", nil, err
		}

		out, err := s.git(ctx, env, nil, "ls-tree", "-r", "-z", tip)
		if err != nil {
			return "", nil, err
		}

		for line := range strings.SplitSeq(out, "\x00") {
			meta, p, ok := strings.Cut(line, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				have[p] = f[2]
			}
		}
	}

	var (
		info     bytes.Buffer
		replaced []string
	)

	for _, e := range edits {
		ids, err := s.edit(ctx, tip, have, e, &info)
		if err != nil {
			return "", nil, err
		}

		replaced = append(replaced, ids...)
	}

	if info.Len() == 0 {
		return tip, nil, nil
	}

	if _, err := s.git(ctx, env, &info, "update-index", "--add", "--remove", "--index-info"); err != nil {
		return "", nil, err
	}

	tree, err := s.git(ctx, env, nil, "write-tree", "--missing-ok")
	if err != nil {
		return "", nil, err
	}

	if strings.TrimSpace(tree) == emptyTree {
		return "", nil, nil
	}

	commit, err := s.commitTree(ctx, message, strings.TrimSpace(tree), tip)

	return commit, replaced, err
}

// emptyTree is git's id of the tree that holds nothing: a ref whose last record went.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// edit adds one record's change to the index lines; a guarded edit whose record changed since it was
// read is a lost race the caller can't win: ErrStale.
func (s *Store) edit(ctx context.Context, tip string, have map[string]string, e groupEdit, info *bytes.Buffer) ([]string, error) {
	dir := e.rec.dir
	cur := ""

	for p := range have {
		if d, tok, ok := strings.Cut(p, marks); ok && d == dir && !tokenTime(path.Base(tok)).IsZero() && !strings.Contains(tok, "/") {
			cur = tok
		}
	}

	// this very write landed already, its answer lost: done
	if cur == e.tok {
		return nil, nil
	}

	if !e.u.Any {
		// a guarded write applies only to the record it read: Old is its
		// directory's tree, or none for one that must not exist yet
		id := ""

		if tip != "" && cur != "" {
			out, err := s.git(ctx, []string{noLazyFetch}, nil, "rev-parse", "--verify", "--quiet", tip+":"+dir)
			if err != nil {
				return nil, err
			}

			id = strings.TrimSpace(out)
		}

		if id != e.u.Old {
			return nil, fmt.Errorf("%w: %s changed under the write", ErrStale, e.u.Ref)
		}
	}

	for p := range have {
		if p == dir+marks+cur || strings.HasPrefix(p, dir+"/") {
			fmt.Fprintf(info, "0 %s\t%s\n", zeroID, p)
		}
	}

	if e.u.Delete {
		return nil, nil
	}

	names := slices.Sorted(maps.Keys(e.u.Files))

	files := map[string][]byte{dir + marks + e.tok: nil}
	for _, n := range names {
		files[dir+"/"+n] = e.u.Files[n]
	}

	ids, err := s.hashObjects(ctx, files)
	if err != nil {
		return nil, err
	}

	var replaced []string

	for _, p := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(info, "100644 %s\t%s\n", ids[p], p)

		if old, ok := have[p]; ok && old != ids[p] {
			replaced = append(replaced, old)
		}
	}

	return replaced, nil
}

// zeroID with mode 0 is how update-index --index-info removes a path.
const zeroID = "0000000000000000000000000000000000000000"

func (s *Store) hashObjects(ctx context.Context, files map[string][]byte) (map[string]string, error) {
	dir, err := os.MkdirTemp(s.Dir, "blobs-")
	if err != nil {
		return nil, fmt.Errorf("blobs: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover in the scratch repository harms nothing

	names := slices.Sorted(maps.Keys(files))

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

	byPath := make(map[string]string, len(names))
	for i, p := range names {
		byPath[p] = ids[i]
	}

	return byPath, nil
}

// commitTree makes a commit of tree on the commits whose files it keeps: a push sends only what those
// don't hold, so the files stay on the remote and never come here.
func (s *Store) commitTree(ctx context.Context, message, tree string, parents ...string) (string, error) {
	nonce, err := newToken(s.now())
	if err != nil {
		return "", err
	}

	var dated []string

	if s.Clock != nil {
		date := fmt.Sprintf("%d +0000", s.Clock().Unix())
		dated = []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	}

	args := []string{"commit-tree", tree, "-m", message, "-m", "nonce " + nonce}

	for _, p := range parents {
		if p != "" {
			args = append(args, "-p", p)
		}
	}

	out, err := s.git(ctx, dated, nil, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

// Compact moves a suite's batches' records into the run's ref in one push deleting the batches' refs,
// each guarded, so a batch writing meanwhile fails it and it goes again; a newer record stays.
func (s *Store) Compact(ctx context.Context, runPrefix, suite string) (int, error) {
	if s.ReadOnly {
		return 0, fmt.Errorf("%w: compact %s", ErrReadOnly, runPrefix)
	}

	defer progress.Stage(ctx, "the store: writing")()

	if s.onBranch() {
		return 0, nil
	}

	s.g.write.Lock()
	defer s.g.write.Unlock()

	r, ok := recordOf(runPrefix + "x")
	if !ok || !strings.HasSuffix(runPrefix, "/") {
		return 0, fmt.Errorf("%w: no run's prefix: %q", ErrGit, runPrefix)
	}

	if old, err := s.oneRefEach(ctx, r.run); err != nil || old {
		return 0, err
	}

	var n int

	err := s.again(ctx, "compaction", func() error {
		var err error

		n, err = s.compactTry(ctx, r.run, suite)

		return err
	})

	return n, err
}

func (s *Store) compactTry(ctx context.Context, run, suite string) (int, error) {
	g, err := s.partial(ctx)
	if err != nil {
		return 0, err
	}

	out, err := g.retry(ctx, []string{lsRemote, refsOnly, g.Remote, BatchPrefix + run + "/" + suite + "/*"})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errTransport, err)
	}

	var batches []string

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			batches = append(batches, ref)
		}
	}

	if len(batches) == 0 {
		return 0, nil
	}

	runRef := RunPrefix + run

	tips, err := g.fetch(ctx, append([]string{runRef}, batches...))
	if err != nil {
		return 0, err
	}

	args := []string{gitPush, porcelain, quiet, atomicPush, g.Remote}
	parents := []string{tips[runRef]}
	moved := map[string]string{}

	for _, b := range batches {
		if tips[b] == "" {
			continue
		}

		entries, err := g.entriesAt(ctx, tips[b])
		if err != nil {
			return 0, err
		}

		newest(moved, entries)

		// the batches' tips are parents too, so the push sends none of their files again
		parents = append(parents, tips[b])
		args = append(args, "--force-with-lease="+b+":"+tips[b], ":"+b)
	}

	commit, err := g.compactCommit(ctx, "compact "+run+" "+suite, tips[runRef], moved, parents)
	if err != nil {
		return 0, err
	}

	if commit != tips[runRef] {
		args = append(args, "--force-with-lease="+runRef+":"+tips[runRef], commit+":"+runRef)
	}

	if err := g.pushGroups(ctx, args); err != nil {
		return 0, err
	}

	return len(batches), nil
}

func newest(moved, batch map[string]string) {
	have := markersOf(moved)

	for dir, tok := range markersOf(batch) {
		if old, ok := have[dir]; ok {
			if !tokenTime(tok).After(tokenTime(old)) {
				continue
			}

			dropRecord(moved, dir, old)
		}

		for p, id := range batch {
			if p == dir+marks+tok || strings.HasPrefix(p, dir+"/") {
				moved[p] = id
			}
		}
	}
}

func dropRecord(files map[string]string, dir, tok string) {
	for p := range files {
		if p == dir+marks+tok || strings.HasPrefix(p, dir+"/") {
			delete(files, p)
		}
	}
}

func markersOf(files map[string]string) map[string]string {
	out := map[string]string{}

	for p := range files {
		if d, tok, ok := strings.Cut(p, marks); ok && !strings.Contains(tok, "/") && !tokenTime(tok).IsZero() {
			out[d] = tok
		}
	}

	return out
}

func (s *Store) compactCommit(ctx context.Context, message, tip string, moved map[string]string, parents []string) (string, error) {
	have, err := s.entriesAt(ctx, tip)
	if err != nil {
		return "", err
	}

	index, err := s.tempIndex()
	if err != nil {
		return "", err
	}

	defer os.Remove(index) //nolint:errcheck // a leftover index in the scratch repository harms nothing

	env := []string{"GIT_INDEX_FILE=" + index, noLazyFetch}

	if tip != "" {
		if _, err := s.git(ctx, env, nil, "read-tree", tip); err != nil {
			return "", err
		}
	}

	var info bytes.Buffer

	moveRecords(have, moved, &info)

	if info.Len() == 0 {
		return tip, nil
	}

	if _, err := s.git(ctx, env, &info, "update-index", "--add", "--remove", "--index-info"); err != nil {
		return "", err
	}

	tree, err := s.git(ctx, env, nil, "write-tree", "--missing-ok")
	if err != nil {
		return "", err
	}

	return s.commitTree(ctx, message, strings.TrimSpace(tree), parents...)
}

func (s *Store) entriesAt(ctx context.Context, commit string) (map[string]string, error) {
	have := map[string]string{}
	if commit == "" {
		return have, nil
	}

	out, err := s.git(ctx, []string{noLazyFetch}, nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}

	for line := range strings.SplitSeq(out, "\x00") {
		meta, p, ok := strings.Cut(line, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 {
			have[p] = f[2]
		}
	}

	return have, nil
}

func moveRecords(have, batch map[string]string, info *bytes.Buffer) {
	runMarks := markersOf(have)

	for dir, tok := range markersOf(batch) {
		if old, ok := runMarks[dir]; ok {
			if !tokenTime(tok).After(tokenTime(old)) {
				continue
			}

			for p := range have {
				if p == dir+marks+old || strings.HasPrefix(p, dir+"/") {
					fmt.Fprintf(info, "0 %s\t%s\n", zeroID, p)
				}
			}
		}

		for p, id := range batch {
			if p == dir+marks+tok || strings.HasPrefix(p, dir+"/") {
				fmt.Fprintf(info, "100644 %s\t%s\n", id, p)
			}
		}
	}
}

// firstRecord fetches a record in one round trip: its own ref, as the layout before the grouped refs wrote
// it, and the grouped refs of its run, preferring its own ref as First always has.
func (s *Store) firstRecord(ctx context.Context, name string) (Ref, bool, error) {
	r, ok := recordOf(name)
	if !ok {
		return Ref{}, false, nil
	}

	if err := s.init(ctx); err != nil {
		return Ref{}, false, err
	}

	// a glob matches the ref and the names it begins, so a missing ref fails nothing; --prune drops
	// what the remote no longer has
	own := mirror + strings.TrimPrefix(name, "refs/")
	prefixes := []string{runsRoot + r.run + "/", plansRoot + r.run}
	specs, locals := groupedSpecs(prefixes)

	args := append([]string{gitFetch, quiet, noTags, prune, noFetchHead, s.Remote, "+" + name + "*:" + own + "*"}, specs...)
	if _, err := s.retry(ctx, args); err != nil {
		return Ref{}, false, err
	}

	refs, err := s.local(ctx, []string{own})
	if err != nil {
		return Ref{}, false, err
	}

	if got, ok := refs[name]; ok {
		s.learnLayout(r, true)

		return got, true, nil
	}

	refs = map[string]Ref{}
	if err := s.groupedRead(ctx, prefixes, locals, refs); err != nil {
		return Ref{}, false, err
	}

	got, ok := refs[name]
	if ok {
		s.learnLayout(r, false)
	}

	return got, ok, nil
}

// learnLayout keeps which layout a run's found plan has, which oneRefEach would ask the remote again:
// a plan is written once, in one layout, and its run is never written in the other.
func (s *Store) learnLayout(r record, own bool) {
	if r.dir != planDir {
		return
	}

	s.g.mu.Lock()
	defer s.g.mu.Unlock()

	if s.g.layouts == nil {
		s.g.layouts = map[string]bool{}
	}

	s.g.layouts[r.run] = own
}
