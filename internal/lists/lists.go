// Package lists reads and writes the lists of what each slice executed, kept
// as refs of the code repository: refs/pipelinegen/lists/<commit> for a
// branch's commits, and the recordings of the runs whose refs still exist.
package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

// The refs this package reads and writes.
const (
	// RefPrefix holds the lists attached to a branch's commits.
	RefPrefix = "refs/pipelinegen/lists/"
	// AttachedPrefix holds, per commit, which runs were attached to it.
	AttachedPrefix = attached.Prefix
	runsPrefix     = "refs/pipelinegen/runs/"
	recordingFile  = "recording.txt"
	listSuffix     = ".txt"
	markSuffix     = ".cannot-tell"
	stateLeaf      = "="
	endPart        = "_end"
)

// Walk is how many first-parent ancestors a lookup tries before it gives
// up and finds no lists: as many as a branch keeps the lists of, so a
// pull request whose base is some way back still finds them.
const Walk = 200

// Ref is the ref that holds the lists attached to a commit.
func Ref(commit string) string { return RefPrefix + commit }

// AttachedRef is the ref that names the runs attached to a commit.
func AttachedRef(commit string) string { return attached.Ref(commit) }

// Source says where a commit's lists came from.
type Source struct {
	Commit string    `json:"commit"`
	Ref    string    `json:"ref,omitempty"`
	From   string    `json:"from,omitempty"`
	Back   int       `json:"back"`
	Live   []LiveRun `json:"live,omitempty"`
	Walk   int       `json:"walk,omitempty"`
	// Gone is set when no commit within the walk has lists: every recorded
	// slice then runs as without a list, and live runs' recordings narrow
	// nothing.
	Gone bool `json:"gone,omitempty"`
	// CannotTell are the slices, by "<suite>/<slice>", whose last recording
	// a correlator couldn't tell: their lists are kept as they were, and
	// planning samples them for that correlator.
	CannotTell Marks `json:"cannot_tell,omitempty"`
	// Replaced is, by slice key, the commit whose run of everything last
	// replaced the list with a whole recording: older runs of that commit's
	// history no longer add to it.
	Replaced map[string]string `json:"-"`
	Written  time.Time         `json:"-"`
}

// Marks are, by slice key, the correlators that couldn't tell what the
// slice touched.
type Marks map[string][]recording.Mark

// add puts marks under key, each plugin once, keeping the earliest time it
// was first seen; a plugin marked already keeps its reason.
func (m Marks) add(key string, marks ...recording.Mark) {
	m.put(key, false, marks...)
}

// renew is add for marks newer than those held: each takes its plugin's
// reason, since a plan prints the newest recording's.
func (m Marks) renew(key string, marks ...recording.Mark) {
	m.put(key, true, marks...)
}

func (m Marks) put(key string, newer bool, marks ...recording.Mark) {
	for _, mk := range marks {
		i := slices.IndexFunc(m[key], func(o recording.Mark) bool { return o.Plugin == mk.Plugin })
		if i < 0 {
			m[key] = append(m[key], mk)

			continue
		}

		if newer && mk.Reason != "" {
			m[key][i].Reason = mk.Reason
		}

		if t := mk.Since; !t.IsZero() && (m[key][i].Since.IsZero() || t.Before(m[key][i].Since)) {
			m[key][i].Since = t
		}
	}
}

func (m Marks) has(key, plugin string) bool {
	return slices.ContainsFunc(m[key], func(o recording.Mark) bool { return o.Plugin == plugin })
}

// LiveRun is a run whose refs still exist and hold recordings.
type LiveRun struct {
	Run    string `json:"run"`
	Change string `json:"change"`
	Commit string `json:"commit"`
	Slices int    `json:"slices"`
}

// ErrLists wraps lists that can't be read or written.
var ErrLists = errors.New("lists")

// Nearest reads the lists of commit: its own ref, else the nearest
// first-parent ancestor's within Walk commits of the repository at repo.
// None found is empty lists, with an empty Source.Ref.
func Nearest(ctx context.Context, store *gitstore.Store, repo, commit string) (recording.Lists, Source, error) {
	return NearestWithin(ctx, store, repo, commit, Walk)
}

// NearestWithin is Nearest within walk first parents: as many as the
// config keeps the lists of (keep.lists.commits).
func NearestWithin(ctx context.Context, store *gitstore.Store, repo, commit string, walk int) (recording.Lists, Source, error) {
	src := Source{Commit: commit, Walk: walk}

	if commit == "" {
		return recording.Lists{}, src, nil
	}

	// a name (a branch, HEAD~1) is asked for by the commit it names: a
	// lists ref is named by a full commit id
	full, ok, err := gitx.Repo{Dir: repo}.Resolve(ctx, commit)
	if err != nil {
		return nil, src, fmt.Errorf("%w: %s: %w", ErrLists, commit, err)
	}

	if ok {
		commit = full
	}

	before, err := runs.FirstParents(ctx, repo, commit, walk)
	if err != nil {
		return nil, src, fmt.Errorf("%w: ancestors of %s: %w", ErrLists, commit, err)
	}

	chain := append([]string{commit}, before...)

	names := make([]string, 0, len(chain))
	for _, c := range chain {
		names = append(names, Ref(c))
	}

	ref, ok, err := store.First(ctx, names)
	if err != nil {
		return nil, src, err
	}

	if !ok {
		return recording.Lists{}, src, nil
	}

	l, marks, replaced, err := read(ctx, store, ref)
	if err != nil {
		return nil, src, err
	}

	if len(marks) > 0 {
		src.CannotTell = marks
	}

	src.Replaced = replaced
	src.Written = ref.Time

	src.Ref = ref.Name
	src.From = strings.TrimPrefix(ref.Name, RefPrefix)
	src.Back = slices.Index(chain, src.From)

	return l, src, nil
}

func read(ctx context.Context, store *gitstore.Store, ref gitstore.Ref) (recording.Lists, Marks, map[string]string, error) {
	files, err := store.Files(ctx, ref.Commit)
	if err != nil {
		return nil, nil, nil, err
	}

	l, marks, err := DecodeMarked(files)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %s: %w", ErrLists, ref.Name, err)
	}

	replaced, err := decodeReplaced(files[replacedFile])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %s: %w", ErrLists, ref.Name, err)
	}

	return l, marks, replaced, nil
}

// replacedFile is the file of a lists ref that says which lists a run of
// everything replaced, and at which commit: the commit, then its slice keys.
// A ref without it has nothing known to be replaced.
const replacedFile = "_replaced.json"

func decodeReplaced(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return map[string]string{}, nil
	}

	var byCommit map[string][]string
	if err := json.Unmarshal(data, &byCommit); err != nil {
		return nil, fmt.Errorf("decode %s: %w", replacedFile, err)
	}

	out := map[string]string{}

	for commit, keys := range byCommit {
		for _, k := range keys {
			out[k] = commit
		}
	}

	return out, nil
}

// encodeReplaced writes slice key to commit as replacedFile holds it, the
// keys of each commit sorted, so the same lists give the same bytes.
func encodeReplaced(replaced map[string]string) ([]byte, error) {
	byCommit := map[string][]string{}
	for k, commit := range replaced {
		byCommit[commit] = append(byCommit[commit], k)
	}

	for _, keys := range byCommit {
		slices.Sort(keys)
	}

	data, err := json.Marshal(byCommit)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", replacedFile, err)
	}

	return append(data, '\n'), nil
}

// Decode reads lists from a ref's files, `<suite>/<slice>.txt` each.
func Decode(files map[string][]byte) (recording.Lists, error) {
	l, _, err := DecodeMarked(files)

	return l, err
}

// DecodeMarked reads lists and their marks from a ref's files: a slice's
// list in `<suite>/<slice>.txt` (an empty one says the slice touched
// nothing a correlator sees), and beside it `<suite>/<slice>.cannot-tell`
// when its last recording couldn't be told.
func DecodeMarked(files map[string][]byte) (recording.Lists, Marks, error) {
	l, marks := recording.Lists{}, Marks{}

	for name, data := range files {
		key, isList := strings.CutSuffix(name, listSuffix)
		if !isList {
			if key, ok := strings.CutSuffix(name, markSuffix); ok && strings.Contains(key, "/") {
				rec, err := recording.Parse(data)
				if err != nil {
					return nil, nil, fmt.Errorf("decode %s: %w", name, err)
				}

				marks.add(key, rec.CannotTell...)
			}

			continue
		}

		// the suite is the first part; a slice named by a test file's
		// path takes the rest, directories included
		if !strings.Contains(key, "/") {
			return nil, nil, fmt.Errorf("%w: %s is no <suite>/<slice>.txt", ErrLists, name)
		}

		rec, err := recording.Parse(data)
		if err != nil {
			return nil, nil, fmt.Errorf("decode %s: %w", name, err)
		}

		l[key] = ids(rec.Own)
	}

	return l, marks, nil
}

func ids(ss []string) []recording.ID {
	out := make([]recording.ID, 0, len(ss))
	for _, s := range ss {
		out = append(out, recording.ID(s))
	}

	return out
}

func strs(ids []recording.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}

	return out
}

// Encode writes lists as a ref's files, `<suite>/<slice>.txt` each. Every
// slice that has a list has a file, an empty list too: it was recorded,
// and touched nothing. A slice without a list has none: no data is never
// an empty list.
func Encode(l recording.Lists) map[string][]byte {
	return EncodeMarked(l, nil)
}

// EncodeMarked writes lists and their marks as a ref's files.
func EncodeMarked(l recording.Lists, marks Marks) map[string][]byte {
	out := make(map[string][]byte, len(l)+len(marks))

	for key, list := range l {
		rec := recording.Recording{Own: strs(list), Observed: len(list) == 0}
		out[key+listSuffix] = rec.Format()
	}

	for key, ms := range marks {
		if len(ms) > 0 {
			out[key+markSuffix] = recording.Recording{CannotTell: ms}.Format()
		}
	}

	return out
}

// Kept says what of one live run's recording of a slice joins a plan's
// lists: its own units, what its batch's setup ran, and its mark. Before and
// At place the run's commit against the lists' commit, which decides whose
// reason a mark shows.
type Kept struct {
	Own, Setup, Mark bool
	Before, At       bool
}

// LiveMark is one live run's mark of a slice, with where the run stands
// against the lists' commit.
type LiveMark struct {
	Key        string
	Mark       recording.Mark
	Before, At bool
}

// Live reads the recordings of every run whose refs still exist, passed,
// failed or still going: unioned into a plan's lists they close the window
// between a merge and its lists, and can only select more. keep (nil:
// everything) leaves out what the lists hold a newer whole recording of.
func Live(ctx context.Context, store *gitstore.Store, keep func(LiveRun, string) Kept) (recording.Lists, []LiveMark, []LiveRun, error) {
	refs, err := store.List(ctx, runsPrefix)
	if err != nil {
		return nil, nil, nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, recordingFile)
	if err != nil {
		return nil, nil, nil, err
	}

	out := recording.Lists{}
	byRun := map[LiveRun]int{}

	var marks []LiveMark

	for name, ref := range refs {
		data, ok := data[ref.Commit]
		if !ok || len(data) == 0 {
			continue
		}

		run, key, ok := recordingOf(name)
		if !ok {
			continue
		}

		rec, err := recording.Parse(data)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %s: %w", ErrLists, name, err)
		}

		kept := Kept{Own: true, Setup: true, Mark: true}
		if keep != nil {
			kept = keep(run, key)
		}

		if len(rec.CannotTell) > 0 && kept.Mark {
			for _, m := range rec.CannotTell {
				marks = append(marks, LiveMark{Key: key, Mark: m, Before: kept.Before, At: kept.At})
			}
		}

		if addKept(out, key, rec, kept) {
			byRun[run]++
		}
	}

	slices.SortStableFunc(marks, func(a, b LiveMark) int {
		if c := a.Mark.Since.Compare(b.Mark.Since); c != 0 {
			return c
		}

		return strings.Compare(a.Key+"\x00"+a.Mark.Plugin+"\x00"+a.Mark.Reason, b.Key+"\x00"+b.Mark.Plugin+"\x00"+b.Mark.Reason)
	})

	live := make([]LiveRun, 0, len(byRun))

	for r, n := range byRun {
		r.Slices = n
		live = append(live, r)
	}

	slices.SortFunc(live, func(a, b LiveRun) int {
		return strings.Compare(a.Run+"/"+a.Change+"/"+a.Commit, b.Run+"/"+b.Change+"/"+b.Commit)
	})

	return recording.Union(out), marks, live, nil
}

func addKept(l recording.Lists, key string, rec recording.Recording, kept Kept) bool {
	if len(rec.CannotTell) > 0 {
		return kept.Mark
	}

	if !kept.Own {
		rec.Own, rec.Observed = nil, false
	}

	if !kept.Setup {
		rec.Setup = nil
	}

	addParsed(l, key, rec)

	return kept.Own || (kept.Setup && len(rec.Setup) > 0)
}

func recordingOf(name string) (LiveRun, string, bool) {
	rest, ok := strings.CutPrefix(name, runsPrefix)
	if !ok {
		return LiveRun{}, "", false
	}

	rest, ok = strings.CutSuffix(rest, "/"+stateLeaf)
	if !ok {
		return LiveRun{}, "", false
	}

	parts := strings.SplitN(rest, "/", 5)
	if len(parts) < 5 || parts[3] == endPart {
		return LiveRun{}, "", false
	}

	change, err1 := unescape(parts[1])
	slice, err2 := unescape(parts[4])

	if err1 != nil || err2 != nil {
		return LiveRun{}, "", false
	}

	return LiveRun{Run: parts[0], Change: change, Commit: parts[2]}, parts[3] + "/" + slice, true
}

// Recordings reads a run's recordings (report.Recordings: the files
// `<suite>/<slice>.txt`) as lists: each slice's own units under its key,
// and what its batch's setup ran under `<suite>/_setup`, the setups of all
// the run's batches together; and the slices a correlator couldn't tell.
// An empty file is no data and adds nothing; a recording the correlators
// finished with nothing in it is an empty list.
func Recordings(files map[string][]byte) (recording.Lists, Marks, error) {
	out, marks := recording.Lists{}, Marks{}

	for name, rec := range files {
		key, ok := strings.CutSuffix(name, listSuffix)
		if !ok || !strings.Contains(key, "/") {
			return nil, nil, fmt.Errorf("%w: %s is no <suite>/<slice>.txt", ErrLists, name)
		}

		if err := addRecording(out, marks, key, rec); err != nil {
			return nil, nil, fmt.Errorf("%w: %s: %w", ErrLists, name, err)
		}
	}

	return recording.Union(out), marks, nil
}

func addRecording(l recording.Lists, marks Marks, key string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	rec, err := recording.Parse(data)
	if err != nil {
		return err
	}

	if len(rec.CannotTell) > 0 {
		marks.add(key, rec.CannotTell...)

		return nil
	}

	addParsed(l, key, rec)

	return nil
}

func addParsed(l recording.Lists, key string, rec recording.Recording) {
	// a finished recording with nothing in it is a list all the same
	if len(rec.Own) > 0 || rec.Observed {
		l[key] = append(l[key], ids(rec.Own)...)
	}

	if len(rec.Setup) > 0 {
		suite, _, _ := strings.Cut(key, "/")
		sk := suite + "/" + recording.SetupList
		l[sk] = append(l[sk], ids(rec.Setup)...)
	}
}

// For reads the lists planning uses for commit: Nearest, with the
// recordings of every live run unioned in where they only add
// (onlyAdding) and the lists hold no newer whole recording (liveKept).
func For(ctx context.Context, store *gitstore.Store, repo, commit string) (recording.Lists, Source, error) {
	return ForWithin(ctx, store, repo, commit, Walk)
}

// ForWithin is For within walk first parents.
func ForWithin(ctx context.Context, store *gitstore.Store, repo, commit string, walk int) (recording.Lists, Source, error) {
	l, src, err := NearestWithin(ctx, store, repo, commit, walk)
	if err != nil {
		return nil, src, err
	}

	if src.Ref == "" && commit != "" {
		src.Gone = true

		return recording.Lists{}, src, nil
	}

	live, marks, runs, err := Live(ctx, store, liveKept(ctx, repo, commit, l, src))
	if err != nil {
		return nil, src, err
	}

	src.Live = runs
	src.CannotTell = withLiveMarks(src.CannotTell, marks, src.Written)

	return recording.Union(l, onlyAdding(live, l)), src, nil
}

func withLiveMarks(listed Marks, live []LiveMark, written time.Time) Marks {
	if len(live) == 0 {
		return listed
	}

	out := Marks{}
	for k, ms := range listed {
		out[k] = slices.Clone(ms)
	}

	for _, lm := range live {
		older := lm.Before || (lm.At && !lm.Mark.Since.After(written))
		if older && listed.has(lm.Key, lm.Mark.Plugin) {
			out.add(lm.Key, lm.Mark)

			continue
		}

		out.renew(lm.Key, lm.Mark)
	}

	return out
}

// onlyAdding is what of the live runs' recordings may join the lists: units
// added to a slice that has a list, and setup lists. A list made from a live
// run, maybe a proposed change's, would bind a list-less slice to less than
// everything.
func onlyAdding(live, l recording.Lists) recording.Lists {
	out := recording.Lists{}

	for key, ids := range live {
		_, listed := l[key]
		_, name, _ := strings.Cut(key, "/")

		if listed || name == recording.SetupList {
			out[key] = ids
		}
	}

	return out
}

// GoneText is the plan's warning when a commit's lists are gone.
func GoneText(src Source) string {
	c := src.Commit
	if len(c) > 12 {
		c = c[:12]
	}

	walk := src.Walk
	if walk == 0 {
		walk = Walk
	}

	return fmt.Sprintf("the lists of %s are gone (none within %d first parents): every recorded slice runs as without a list", c, walk)
}

// Bound is the lists as planning binds them at its level. With the setup
// split on, each slice keeps only what its own tests ran, and each suite's
// setup list comes back apart, to bind the suite's setup (which a sample
// covers). With it off, a suite's setup list is in the list of every slice
// of that suite that has one: setup code binds the slices it ran for. A
// slice without a list stays without: it is bound to everything anyway.
func Bound(l recording.Lists, split bool) (recording.Lists, map[string][]recording.ID) {
	out, setup := recording.Lists{}, map[string][]recording.ID{}

	for key, list := range l {
		suite, name, _ := strings.Cut(key, "/")
		if name == recording.SetupList {
			setup[suite] = list

			continue
		}

		out[key] = list
	}

	if split {
		return out, setup
	}

	for key, list := range out {
		suite, _, _ := strings.Cut(key, "/")
		if s := setup[suite]; len(s) > 0 {
			merged := slices.Concat(list, s)
			slices.Sort(merged)
			out[key] = slices.Compact(merged)
		}
	}

	return out, map[string][]recording.ID{}
}

// liveKept says what of each live run's recordings joins the lists of
// commit. A mark is kept only for a run of commit's own history the lists
// hold no newer told recording of. Units are kept unless a run of everything
// replaced the slice's list at or after the run's commit: that whole
// recording already dropped what the test no longer runs.
func liveKept(ctx context.Context, repo, commit string, l recording.Lists, src Source) func(LiveRun, string) Kept {
	if repo == "" || commit == "" {
		return nil
	}

	told := toldSince(ctx, repo, l, src)
	precedes := ancestry(ctx, repo)

	return func(r LiveRun, key string) Kept {
		suite, _, _ := strings.Cut(key, "/")
		kept := Kept{
			Own:   !replacedSince(src.Replaced[key], r, precedes),
			Setup: !replacedSince(src.Replaced[suite+"/"+recording.SetupList], r, precedes),
			Mark:  true,
		}

		if src.From != "" {
			if strings.HasPrefix(src.From, r.Commit) {
				kept.At = true
			} else if ok, known := precedes(r.Commit, src.From); ok && known {
				kept.Before = true
			}
		}

		switch ok, known := precedes(r.Commit, commit); {
		case known && !ok, told != nil && told(r, key):
			kept.Mark = false
		case !kept.Own:
			// the list was replaced by a recording that told the slice on
			// a tree that holds the run's: the run's attempt failing to
			// tell says nothing the lists don't know better
			kept.Mark = false
		}

		return kept
	}
}

func replacedSince(at string, r LiveRun, precedes func(a, b string) (ok, known bool)) bool {
	if at == "" {
		return false
	}

	if strings.HasPrefix(at, r.Commit) {
		return true
	}

	ok, known := precedes(r.Commit, at)

	return ok && known
}

func ancestry(ctx context.Context, repo string) func(a, b string) (ok, known bool) {
	type answer struct{ ok, known bool }

	seen := map[[2]string]answer{}

	return func(a, b string) (bool, bool) {
		got, ok := seen[[2]string{a, b}]
		if !ok {
			is, err := gitx.Repo{Dir: repo}.IsAncestor(ctx, a, b)
			got = answer{ok: is, known: err == nil}
			seen[[2]string{a, b}] = got
		}

		return got.ok, got.known
	}
}

func toldSince(ctx context.Context, repo string, l recording.Lists, src Source) func(LiveRun, string) bool {
	if repo == "" || src.From == "" {
		return nil
	}

	return func(r LiveRun, key string) bool {
		// a list the attached lists still mark wasn't told there either
		if _, listed := l[key]; !listed || len(src.CannotTell[key]) > 0 || strings.HasPrefix(src.From, r.Commit) {
			return false
		}

		ok, err := gitx.Repo{Dir: repo}.IsAncestor(ctx, r.Commit, src.From)

		return ok && err == nil
	}
}
