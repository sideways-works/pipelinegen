package lists

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Attach is one call of `lists attach`.
type Attach struct {
	Repo                string
	Run, Change, Commit string
	To                  string
	Recordings          recording.Lists
	// CannotTell are the run's slices that a correlator couldn't tell:
	// their lists stay as they are, marked until a run tells them.
	CannotTell Marks
	// Trusted says the run was not a proposed change's, by the trusted
	// config: only then may its recordings shrink a list (Shrinkable),
	// tell a marked slice, or make a list for a slice that had none.
	// Anything else only adds units to lists that exist.
	Trusted bool
	Walk    int
	// Shrinkable are the slice keys whose recording may replace their
	// list: slices that passed in a run that selected every slice. Every
	// other recording only adds to its list.
	Shrinkable map[string]bool
	// Whole are the slice keys whose recording is whole: slices that passed.
	// Only such a recording makes a list or tells a marked slice: a failed
	// slice may have stopped part way, and a list of what it reached would
	// narrow it to less than it runs.
	Whole map[string]bool
	// SuitesPassed, when set, names the suites every selected slice of which
	// passed: only their setup list may shrink, since a suite with a slice
	// that failed or never ran may have had a batch whose setup the run
	// never saw. Nil judges by the recordings alone.
	SuitesPassed map[string]bool
	Seed         *attached.Seed
	KeepMarks    bool
}

// Attached is what Attach did.
type Attached struct {
	To            string
	Base          Source
	Again         bool
	Slices, Grown int
}

const tries = 5

// ErrAttach wraps what keeps Attach from attaching.
var ErrAttach = errors.New("attach")

// Do merges the run's recordings into To's lists by recording.Merge's rules
// and names the run among the lists' attached runs, in one write; without
// lists of its own To starts from its nearest ancestor's. A concurrent
// attach to the same commit makes it read again, and the same run attached
// again writes nothing new. It never deletes.
func (a Attach) Do(ctx context.Context, store *gitstore.Store) (*Attached, error) {
	if a.Run == "" || a.Change == "" || a.Commit == "" || a.To == "" {
		return nil, fmt.Errorf("%w: run, change, commit and target are all needed", ErrAttach)
	}

	to, err := a.resolve(ctx)
	if err != nil {
		return nil, err
	}

	me := Attachment{Run: a.Run, Change: a.Change, Commit: a.Commit, Seed: a.Seed}

	for range tries {
		got, err := a.once(ctx, store, to, me)
		if errors.Is(err, gitstore.ErrStale) {
			continue
		}

		return got, err
	}

	return nil, fmt.Errorf("%w: the lists of %s kept moving under %d tries", ErrAttach, to, tries)
}

func (a Attach) walk() int {
	if a.Walk > 0 {
		return a.Walk
	}

	return Walk
}

// resolve turns To into a full commit of the checkout, refusing what the
// checkout can't follow back: lists built without the ancestors' would
// drop what they hold.
func (a Attach) resolve(ctx context.Context) (string, error) {
	repo := gitx.Repo{Dir: a.Repo}

	to, ok, err := repo.Resolve(ctx, a.To)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrAttach, err)
	}

	if !ok {
		return "", fmt.Errorf("%w: no commit %s in the checkout at %s: fetch it first", ErrAttach, a.To, a.Repo)
	}

	shallow, err := isShallow(ctx, a.Repo)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrAttach, err)
	}

	if shallow {
		return "", fmt.Errorf("%w: the checkout at %s is shallow, so the lists of %s's ancestors can't be found: fetch its history", ErrAttach, a.Repo, to[:12])
	}

	return to, nil
}

func (a Attach) once(ctx context.Context, store *gitstore.Store, to string, me Attachment) (*Attached, error) {
	own, _, err := store.First(ctx, []string{Ref(to)})
	if err != nil {
		return nil, err
	}

	// who went into the lists so far: under the lists ref, and in the
	// ref lists written before kept it in
	who, err := attachedFiles(ctx, store, own, to)
	if err != nil {
		return nil, err
	}

	_, again := who[attached.Dir+me.File()]
	if again && !a.Trusted {
		return &Attached{To: to, Again: true}, nil
	}

	var (
		base      recording.Lists
		baseMarks Marks
		replaced  map[string]string
		src       Source
	)

	if own.Commit != "" {
		if base, baseMarks, replaced, err = read(ctx, store, own); err != nil {
			return nil, err
		}

		src = Source{Commit: to, Ref: own.Name, From: to}
	} else if base, src, err = NearestWithin(ctx, store, a.Repo, to, a.walk()); err != nil {
		return nil, err
	} else {
		baseMarks, replaced = src.CannotTell, src.Replaced
	}

	in := a.taken(ctx, again, base, replaced)
	out := &Attached{To: to, Base: src}

	merged, replacing := merge(base, in.recordings, in.shrink, in.whole, a.SuitesPassed)
	marks := mergeMarks(baseMarks, in.told, in.cannot)
	nowReplaced := replacedAt(replaced, replacing, merged, a.Commit)

	// a run attached before writes only what it has that the lists don't
	if again && sameLists(base, baseMarks, replaced, merged, marks, nowReplaced) {
		return &Attached{To: to, Again: true}, nil
	}

	out.Slices = len(merged)

	for k, ids := range merged {
		_, had := base[k]
		if !had || !slices.Equal(ids, base[k]) {
			out.Grown++
		}
	}

	note, err := json.Marshal(me)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAttach, err)
	}

	files := EncodeMarked(merged, marks)
	maps.Copy(files, who)

	// the lists this run replaced are whole recordings of the commit it
	// tested: older runs of that commit's history add nothing to them
	if len(nowReplaced) > 0 {
		if files[replacedFile], err = encodeReplaced(nowReplaced); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrAttach, err)
		}
	}

	files[attached.Dir+me.File()] = append(note, '\n')

	message := "lists " + to[:12] + " from " + a.Run + " " + a.Change + " " + a.Commit
	if _, err := store.Write(ctx, message, gitstore.Update{Ref: Ref(to), Old: own.Commit, Files: files}); err != nil {
		return nil, err
	}

	return out, nil
}

type taking struct {
	recordings recording.Lists
	shrink     map[string]bool
	whole      map[string]bool
	told       recording.Lists
	cannot     Marks
}

// taken is what of the run goes into lists that hold base. A trusted run
// attached again may bring slices a rerun passed since: in a run of
// everything they replace their list, in one that adds they add, and the
// marks of the slices that passed go in again.
func (a Attach) taken(ctx context.Context, again bool, base recording.Lists, replaced map[string]string) taking {
	in := taking{
		recordings: a.Recordings, shrink: a.Shrinkable, whole: a.Whole,
		told: wholeOnly(a.Recordings, a.Whole), cannot: a.CannotTell,
	}

	switch {
	case again && len(a.Shrinkable) > 0:
		in.recordings, in.shrink, in.whole = a.passedSince(ctx, in.recordings, replaced)
		in.told = wholeOnly(in.recordings, in.whole)
	case again:
		in.told = nil
	}

	if !a.Trusted {
		// a run that isn't trusted (a proposed change's) may only add:
		// to lists that exist, never shrinking one, never telling a
		// marked slice, never making a list
		in.recordings, in.shrink, in.told = onlyAdding(a.Recordings, base), nil, nil
	}

	if a.KeepMarks {
		in.told = nil
	}

	return in
}

func sameLists(before recording.Lists, beforeMarks Marks, beforeReplaced map[string]string, after recording.Lists, afterMarks Marks, afterReplaced map[string]string) bool {
	if !maps.EqualFunc(EncodeMarked(before, beforeMarks), EncodeMarked(after, afterMarks), bytes.Equal) {
		return false
	}

	return maps.Equal(beforeReplaced, afterReplaced)
}

func attachedFiles(ctx context.Context, store *gitstore.Store, own gitstore.Ref, to string) (map[string][]byte, error) {
	who := map[string][]byte{}

	if own.Commit != "" {
		got, err := store.Under(ctx, own.Commit, attached.Dir)
		if err != nil {
			return nil, err
		}

		maps.Copy(who, got)
	}

	old, ok, err := store.First(ctx, []string{AttachedRef(to)})
	if err != nil || !ok {
		return who, err
	}

	got, err := store.Files(ctx, old.Commit)
	if err != nil {
		return nil, err
	}

	for name, data := range got {
		who[attached.Dir+name] = data
	}

	return who, nil
}

// mergeMarks keeps the marks of the slices this run didn't tell, drops those
// it told, and adds the run's own: a slice marked before keeps the time it
// was first seen and takes the run's newest reason.
func mergeMarks(base Marks, told recording.Lists, cannot Marks) Marks {
	out := Marks{}

	for k, ms := range base {
		if _, ok := told[k]; !ok {
			out.add(k, ms...)
		}
	}

	for k, ms := range cannot {
		out.renew(k, ms...)
	}

	return out
}

// passedSince is what of a run attached before still goes into the lists:
// the recordings of the slices that passed in a run of everything since it
// was first attached, and their suite's setup list once the suite passed
// whole. A mark the run left is not put back, since the lists may have been
// told since.
func (a Attach) passedSince(ctx context.Context, recs recording.Lists, replaced map[string]string) (recording.Lists, map[string]bool, map[string]bool) {
	precedes := ancestry(ctx, a.Repo)
	run := LiveRun{Commit: a.Commit}

	out, shrink, whole := recording.Lists{}, map[string]bool{}, map[string]bool{}

	for k, ids := range recs {
		suite, name, _ := strings.Cut(k, "/")
		if replacedSince(replaced[k], run, precedes) {
			continue
		}

		switch {
		case name == recording.SetupList:
			if a.SuitesPassed[suite] {
				out[k] = ids
			}
		case a.Shrinkable[k] && a.Whole[k]:
			out[k], shrink[k], whole[k] = ids, true, true
		}
	}

	// a setup list alone, with no slice of its suite newly passed, has
	// nothing new to say
	newly := map[string]bool{}

	for k := range shrink {
		suite, _, _ := strings.Cut(k, "/")
		newly[suite] = true
	}

	for k := range out {
		if suite, name, _ := strings.Cut(k, "/"); name == recording.SetupList && !newly[suite] {
			delete(out, k)
		}
	}

	return out, shrink, whole
}

// merge folds recordings into lists: those in shrink replace their list, the
// rest only add to it. A suite's setup list may shrink only when every
// recorded slice of the suite may and every selected slice passed. A
// recording makes a list for a slice that had none only when it is whole: a
// failed slice may have stopped part way.
func merge(base, recordings recording.Lists, shrink, whole, suitesPassed map[string]bool) (merged, replaced recording.Lists) {
	replace, add := recording.Lists{}, recording.Lists{}
	setupShrinks := map[string]bool{}

	for k := range recordings {
		suite, name, _ := strings.Cut(k, "/")
		if name == recording.SetupList {
			continue
		}

		if ok, seen := setupShrinks[suite]; !seen || ok {
			setupShrinks[suite] = shrink[k] && (suitesPassed == nil || suitesPassed[suite])
		}
	}

	for k, ids := range recordings {
		suite, name, _ := strings.Cut(k, "/")

		switch {
		case len(ids) == 0 && name == recording.SetupList:
		case name == recording.SetupList && setupShrinks[suite]:
			replace[k] = ids
		case name == recording.SetupList:
			add[k] = ids
		case shrink[k]:
			replace[k] = ids
		default:
			if _, listed := base[k]; listed || whole[k] {
				add[k] = ids
			}
		}
	}

	return recording.Merge(recording.Merge(base, replace, true), add, false), replace
}

func replacedAt(before map[string]string, replacing, merged recording.Lists, commit string) map[string]string {
	out := map[string]string{}

	for k, at := range before {
		if _, listed := merged[k]; listed {
			out[k] = at
		}
	}

	for k := range replacing {
		if _, listed := merged[k]; listed {
			out[k] = commit
		}
	}

	return out
}

// Attachment names a run whose recordings went into a commit's lists.
type Attachment = attached.Attachment

// Attachments reads which runs were attached to commit; see attached.Read.
func Attachments(ctx context.Context, store *gitstore.Store, commit string) ([]Attachment, error) {
	got, err := attached.Read(ctx, store, commit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLists, err)
	}

	return got, nil
}

func unescape(part string) (string, error) {
	s, err := url.PathUnescape(part)
	if err != nil {
		return "", fmt.Errorf("%w: ref part %q", ErrLists, part)
	}

	return s, nil
}

func isShallow(ctx context.Context, repo string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--is-shallow-repository") //nolint:gosec // git in the checkout the command was given
	cmd.Env = secret.HardenGit(os.Environ())

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git rev-parse in %s: %w", repo, err)
	}

	return strings.TrimSpace(string(out)) == "true", nil
}

func wholeOnly(recs recording.Lists, whole map[string]bool) recording.Lists {
	out := recording.Lists{}

	for key, ids := range recs {
		if whole[key] {
			out[key] = ids
		}
	}

	return out
}
