package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

// ID names one run of one change at one commit: where its refs live and
// where it reports.
type ID struct {
	Run    string
	Change string
	Commit string
	PR     int
	AlsoTo []int
	// Proposed marks a run whose change is proposed, never trusted: what
	// it records is never attached as a trusted branch's lists.
	Proposed bool
}

// ErrID wraps a run, change, suite or slice name that can't name a ref.
var ErrID = errors.New("bad name")

func checkName(what, s string) error {
	if s == "" || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "_") || strings.HasSuffix(s, ".lock") ||
		strings.Contains(s, "..") || strings.ContainsAny(s, "/\\ ~^:?*[\n\t\x00") {
		return fmt.Errorf("%w: %s %q", ErrID, what, s)
	}

	return nil
}

func (id ID) Check() error {
	return errors.Join(checkName("run", id.Run), checkText("change", id.Change), checkName("commit", id.Commit))
}

const refRoot = "refs/pipelinegen/"

// Prefix is where every ref of the run lives.
func (id ID) Prefix() string {
	return refRoot + "runs/" + id.Run + "/" + RefPart(id.Change) + "/" + short(id.Commit) + "/"
}

// StateRef is one slice's ref. Its last part is always "=", which no encoded
// name part is, so one slice's ref is never another's directory.
func (id ID) StateRef(suite, slice string) string {
	return id.Prefix() + suite + "/" + RefPath(slice) + "/" + stateLeaf
}

const stateLeaf = "="

func (id ID) sliceOf(ref string) (suite, slice string, ok bool, err error) {
	rest, isState := strings.CutSuffix(strings.TrimPrefix(ref, id.Prefix()), "/"+stateLeaf)
	if !isState || !strings.HasPrefix(ref, id.Prefix()) {
		return "", "", false, nil
	}

	suite, enc, _ := strings.Cut(rest, "/")

	slice, err = NameFromRef(enc)
	if err != nil {
		return "", "", false, err
	}

	return suite, slice, true, nil
}

func checkText(what, s string) error {
	if s == "" || strings.ContainsAny(s, "\n\x00") {
		return fmt.Errorf("%w: %s %q", ErrID, what, s)
	}

	return nil
}

func checkSlice(name string) error {
	if name == "" || strings.ContainsAny(name, "\n\x00") {
		return fmt.Errorf("%w: slice %q", ErrID, name)
	}

	for part := range strings.SplitSeq(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: slice %q", ErrID, name)
		}
	}

	// a batch's journal lives in result/.pipelinegen, where this slice's
	// result and extras would go
	if first, _, _ := strings.Cut(name, "/"); first == JournalDir {
		return fmt.Errorf("%w: slice %q: %s is pipelinegen's own directory", ErrID, name, JournalDir)
	}

	return nil
}

func refByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("-_.+,", b) >= 0
}

// RefPath writes a name as ref path parts: every byte a ref can't hold is %XX, and so is each
// dot that would end a part, start it, double it or make it end in ".lock".
func RefPath(name string) string {
	parts := strings.Split(name, "/")

	for i, part := range parts {
		parts[i] = RefPart(part)
	}

	return strings.Join(parts, "/")
}

// RefPart turns a name into one ref part, the way RefPath does each part;
// a slash in it is written %2F, so a branch release/1.2 stays one part.
func RefPart(part string) string {
	var b strings.Builder

	for j := range len(part) {
		c := part[j]

		dot := c == '.' && (j == 0 || j == len(part)-1 || part[j-1] == '.' || strings.HasPrefix(part[j:], ".lock") && j+5 == len(part))
		if refByte(c) && !dot {
			b.WriteByte(c)

			continue
		}

		fmt.Fprintf(&b, "%%%02X", c)
	}

	return b.String()
}

func NameFromRef(path string) (string, error) {
	name, err := url.PathUnescape(path)
	if err != nil {
		return "", fmt.Errorf("%w: ref path %q", ErrID, path)
	}

	return name, nil
}

// EndRef marks a suite's end; suite names never start with "_".
func (id ID) EndRef(suite string) string { return id.Prefix() + "_end/" + suite }

func (id ID) PlanRef() string {
	return refRoot + "plans/" + id.Run + "/" + RefPart(id.Change) + "/" + short(id.Commit)
}

// AllPlans is where every run's plans live: plans/<run>/<change>/<commit>.
const AllPlans = refRoot + "plans/"

func PlansOf(run string) string { return refRoot + "plans/" + run + "/" }

func PlansOfChange(run, change string) string { return PlansOf(run) + RefPart(change) + "/" }

// SweptOf is where a run's sweep keeps its marks on the heads it took up.
func SweptOf(run string) string { return refRoot + "swept/" + run + "/" }

// SweptRef is the sweep's mark on one head: never under the run's records,
// whose presence says a suite began.
func SweptRef(id ID) string { return SweptOf(id.Run) + RefPart(id.Change) + "/" + short(id.Commit) }

func RunsOf(run string) string { return refRoot + "runs/" + run + "/" }

// AlsoToRef names more pull requests for a change's comment.
func AlsoToRef(run, change string) string { return refRoot + "also-to/" + run + "/" + RefPart(change) }

const (
	stateFile     = "state.json"
	recordingFile = "recording.txt"
	endFile       = "end.json"
	extraDir      = "extra/"
	planFileName  = "plan.json"
	alsoToFile    = "also_to.json"
)

// SliceName is a slice's name from its label, as the contracts define it:
// //e2e/console:search is e2e.console.search.
func SliceName(label string) string {
	return strings.NewReplacer("/", ".", ":", ".").Replace(strings.TrimPrefix(label, "//"))
}

// WholeName is the slice name a whole-suite check reports under.
func WholeName(check string) string { return "whole." + check }

// StateFile is one slice's state, written only by that slice's own steps.
type StateFile struct {
	Label    string    `json:"label"`
	State    State     `json:"state"`
	URL      string    `json:"url,omitempty"`
	Updated  time.Time `json:"updated"`
	Failures []Failure `json:"failures,omitempty"`
	Seconds  float64   `json:"seconds,omitempty"`
	// Tests is 0 for a passed slice that ran no tests, and absent
	// otherwise.
	Tests      *int             `json:"tests,omitempty"`
	Recorded   bool             `json:"recorded,omitempty"`
	CannotTell []recording.Mark `json:"cannot_tell,omitempty"`
	// Files are the failure files the ref holds under files/, Dropped
	// those the result named that it doesn't, with why.
	Files   []KeptFile    `json:"files,omitempty"`
	Dropped []DroppedFile `json:"dropped_files,omitempty"`
	// CPUSeconds and PeakMB are what the slice used of its batch's cgroup: CPU, and the most memory the
	// batch held while it ran.
	CPUSeconds float64 `json:"cpu_seconds,omitempty"`
	// CPUWall is the seconds CPUSeconds was read over, the slice's parts' time; none in older states.
	CPUWall float64 `json:"cpu_wall,omitempty"`
	PeakMB  int64   `json:"peak_mb,omitempty"`
}

type endMark struct {
	Ended time.Time `json:"ended"`
	Why   string    `json:"why,omitempty"`
}

type alsoTo struct {
	AlsoTo []int `json:"also_to"`
}

type planFile struct {
	Base       string         `json:"base"`
	Head       string         `json:"head"`
	Merged     string         `json:"merged"`
	All        bool           `json:"all"`
	Narrowing  int            `json:"narrowing"`
	Narrowings []NarrowingSet `json:"narrowings"`
	Suites     []planSuite    `json:"suites"`
	Plugins    *Plugins       `json:"plugins,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Rebuild    *Rebuild       `json:"rebuild,omitempty"`
	Lists      *struct {
		CannotTell map[string][]recording.Mark `json:"cannot_tell"`
	} `json:"lists,omitempty"`
}

type planSuite struct {
	Suite    string `json:"suite"`
	All      bool   `json:"all"`
	Reason   string `json:"reason"`
	Selected []struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	} `json:"selected"`
	Untouched  []string            `json:"untouched"`
	WholeSuite []string            `json:"whole_suite"`
	Reasons    map[string][]Reason `json:"reasons"`
	Removed    []string            `json:"removed"`
	Narrowed   []Narrowed          `json:"narrowed"`
	Causes     []Cause             `json:"causes"`
}

var ErrNoPlan = errors.New("no plan")

func parsePlan(data []byte) (*planFile, error) {
	if len(data) == 0 {
		return nil, ErrNoPlan
	}

	var plan planFile
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}

	return &plan, nil
}

// ofRun keeps the suites the run covers: an older planner's plan holds every suite of the
// config, and a suite the run has no job for would keep the run from ever ending.
func (p *planFile) ofRun(cfg *config.Config, run string) {
	p.Suites = slices.DeleteFunc(p.Suites, func(sp planSuite) bool { return !cfg.RunCovers(run, sp.Suite) })
}

// ErrPlanned is a plan written for a run that has one: another planner
// wrote it first, and its plan stands.
var ErrPlanned = errors.New("planned already")

// WritePlan stores the run's plan once: a run keeps the first plan written for it, so suite
// jobs never see it change; a planner that finds one written meanwhile gets ErrPlanned.
func WritePlan(ctx context.Context, store *gitstore.Store, id ID, plan []byte) error {
	err := writePlan(ctx, store, id, plan, false)
	if errors.Is(err, gitstore.ErrStale) {
		return fmt.Errorf("%w: %s", ErrPlanned, id.PlanRef())
	}

	return err
}

// ReplacePlan stores the run's plan whatever the run holds: a local
// recording, run again at the same commit, plans afresh.
func ReplacePlan(ctx context.Context, store *gitstore.Store, id ID, plan []byte) error {
	return writePlan(ctx, store, id, plan, true)
}

func writePlan(ctx context.Context, store *gitstore.Store, id ID, plan []byte, replace bool) error {
	if err := id.Check(); err != nil {
		return err
	}

	if _, err := parsePlan(plan); err != nil {
		return err
	}

	_, err := store.Write(ctx, "plan "+id.Prefix(), gitstore.Update{
		Ref: id.PlanRef(), Any: replace, Files: map[string][]byte{planFileName: plan},
	})

	return err
}

type item struct {
	suite, name, label string
}

func items(sp planSuite) []item {
	out := make([]item, 0, len(sp.Selected)+len(sp.WholeSuite))
	for _, sl := range sp.Selected {
		name := sl.Name
		if name == "" {
			name = SliceName(sl.Label)
		}

		label := sl.Label
		if label == "" {
			label = name
		}

		out = append(out, item{suite: sp.Suite, name: name, label: label})
	}

	for _, w := range sp.WholeSuite {
		out = append(out, item{suite: sp.Suite, name: WholeName(w), label: WholeName(w)})
	}

	return out
}

type snapshot struct {
	plan   *planFile
	refs   map[string]gitstore.Ref
	states map[string]StateFile // by "<suite>/<slice>"
	ends   map[string]time.Time // by suite
	whys   map[string]string    // by suite: why its end left slices without a result
	alsoTo []int
}

func (s *snapshot) same(o snapshot) bool {
	return maps.EqualFunc(s.refs, o.refs, func(a, b gitstore.Ref) bool { return a.Commit == b.Commit })
}

// PlansPrefix is where a change's plans live, one ref per planned commit.
func PlansPrefix(run, change string) string {
	return refRoot + "plans/" + run + "/" + RefPart(change) + "/"
}

// Ends are the suites of a run that have their end mark, each with when it
// ended as the mark says (zero for a mark that doesn't say).
func Ends(ctx context.Context, store *gitstore.Store, cfg *config.Config, id ID) (map[string]time.Time, error) {
	snap, err := readSnapshot(ctx, store, cfg, id)
	if err != nil {
		return nil, err
	}

	return snap.ends, nil
}

func readSnapshot(ctx context.Context, store *gitstore.Store, cfg *config.Config, id ID) (snapshot, error) {
	alsoPrefix := refRoot + "also-to/" + id.Run + "/"
	planPrefix := PlansPrefix(id.Run, id.Change)

	refs, err := store.List(ctx, id.Prefix(), alsoPrefix, planPrefix)
	if err != nil {
		return snapshot{}, err
	}

	snap := snapshot{refs: map[string]gitstore.Ref{}, states: map[string]StateFile{}, ends: map[string]time.Time{}}

	var stateCommits, endCommits []string

	for name, ref := range refs {
		switch {
		case name == AlsoToRef(id.Run, id.Change), name == id.PlanRef():
			snap.refs[name] = ref
		// end marks lie under the run's prefix too, so they are told apart first
		case strings.HasPrefix(name, id.EndRef("")):
			snap.refs[name] = ref
			endCommits = append(endCommits, ref.Commit)
		case strings.HasPrefix(name, id.Prefix()):
			snap.refs[name] = ref
			stateCommits = append(stateCommits, ref.Commit)
		}
	}

	if err := snap.readStates(ctx, store, id, stateCommits); err != nil {
		return snapshot{}, err
	}

	if err := snap.readEnds(ctx, store, id, endCommits); err != nil {
		return snapshot{}, err
	}

	ref, ok := snap.refs[id.PlanRef()]
	if !ok {
		return snapshot{}, fmt.Errorf("%w: %s is missing", ErrNoPlan, id.PlanRef())
	}

	raw, _, err := store.File(ctx, ref.Commit, planFileName)
	if err != nil {
		return snapshot{}, err
	}

	if snap.plan, err = parsePlan(raw); err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", id.PlanRef(), err)
	}

	snap.plan.ofRun(cfg, id.Run)

	if ref, ok := snap.refs[AlsoToRef(id.Run, id.Change)]; ok {
		data, _, err := store.File(ctx, ref.Commit, alsoToFile)
		if err != nil {
			return snapshot{}, err
		}

		var a alsoTo
		if err := json.Unmarshal(data, &a); err != nil {
			return snapshot{}, fmt.Errorf("%s: %w", AlsoToRef(id.Run, id.Change), err)
		}

		snap.alsoTo = a.AlsoTo
	}

	return snap, nil
}

func (s *snapshot) readStates(ctx context.Context, store *gitstore.Store, id ID, commits []string) error {
	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return err
	}

	for name, ref := range s.refs {
		raw, ok := data[ref.Commit]
		if !ok || !strings.HasPrefix(name, id.Prefix()) {
			continue
		}

		var sf StateFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			// one broken ref must not hide the rest; it shows as errored
			sf = StateFile{State: Errored, Failures: []Failure{{Test: "(unreadable state)", Tail: err.Error()}}}
		}

		suite, slice, isState, err := id.sliceOf(name)
		if err != nil {
			return err
		}

		if isState {
			s.states[suite+"/"+slice] = sf
		}
	}

	return nil
}

func (s *snapshot) readEnds(ctx context.Context, store *gitstore.Store, id ID, commits []string) error {
	data, err := store.FileEach(ctx, commits, endFile)
	if err != nil {
		return err
	}

	for name, ref := range s.refs {
		raw, ok := data[ref.Commit]
		if !ok || !strings.HasPrefix(name, id.EndRef("")) {
			continue
		}

		var e endMark
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		s.ends[strings.TrimPrefix(name, id.EndRef(""))] = e.Ended

		if e.Why != "" {
			if s.whys == nil {
				s.whys = map[string]string{}
			}

			s.whys[strings.TrimPrefix(name, id.EndRef(""))] = e.Why
		}
	}

	return nil
}

var noResult = Failure{Test: "(no result)", Tail: "the slice never reported how it ended"}

var notRun = Failure{Test: "(not run)", Tail: "its batch ended before any slice started: the batch's setup failed"}

func buildRun(id ID, snap snapshot, groups map[string]*regexp.Regexp) Run {
	r := Run{Name: id.Run, Change: id.Change, Commit: id.Commit, Plugins: snap.plan.Plugins, All: snap.plan.All, PlanWarnings: snap.plan.Warnings, Rebuild: snap.plan.Rebuild, Narrowings: snap.plan.Narrowings}
	if snap.plan.Lists != nil {
		r.Marked = snap.plan.Lists.CannotTell
	}

	for _, sp := range snap.plan.Suites {
		s := Suite{Name: sp.Suite, Untouched: sp.Untouched, Removed: sp.Removed, Narrowed: sp.Narrowed, Causes: sp.Causes}
		if sp.All {
			s.Reason = sp.Reason
		}

		ended, over := snap.ends[sp.Suite]

		for _, it := range items(sp) {
			sf, ok := snap.states[it.suite+"/"+it.name]
			if !ok {
				sf = StateFile{State: Pending}
			}

			sl := Slice{Name: it.name, Label: it.label, Group: groupOf(groups[sp.Suite], it.name), State: sf.State, URL: sf.URL, Failures: sf.Failures, Reasons: sp.Reasons[it.label]}
			sl.NoTests = sf.State == Passed && sf.Tests != nil && *sf.Tests == 0
			sl.Recorded, sl.CannotTell, sl.Files, sl.PeakMB = sf.Recorded, sf.CannotTell, len(sf.Files), sf.PeakMB

			sl.Cores = sf.cores()

			if over && (sl.State == Pending || sl.State == Running) && !sf.Updated.After(ended) {
				f := noResult
				if why := snap.whys[sp.Suite]; why != "" {
					f.Tail = why
				}

				sl.State, sl.Failures = Errored, []Failure{f}
			}

			s.Slices = append(s.Slices, sl)
		}

		r.Suites = append(r.Suites, s)
	}

	return r
}

// States says where each selected slice and whole-suite check of a suite stands, by name: one
// the suite's end left without a result is errored, one that never started pending.
func States(ctx context.Context, store *gitstore.Store, id ID, suite string) (map[string]State, error) {
	if err := errors.Join(id.Check(), checkName("suite", suite)); err != nil {
		return nil, err
	}

	snap, err := readSnapshot(ctx, store, nil, id)
	if err != nil {
		return nil, err
	}

	out := map[string]State{}

	for _, s := range buildRun(id, snap, nil).Suites {
		if s.Name != suite {
			continue
		}

		for _, sl := range s.Slices {
			out[sl.Name] = sl.State
		}
	}

	return out, nil
}

var leadingWord = regexp.MustCompile(`^[A-Za-z0-9]+`)

func groupOf(pattern *regexp.Regexp, name string) string {
	if pattern != nil {
		if m := pattern.FindStringSubmatch(name); len(m) > 1 && m[1] != "" {
			return m[1]
		}
	}

	base := path.Base(name)
	if w := leadingWord.FindString(base); w != "" {
		return w
	}

	return base
}

func WriteAlsoTo(ctx context.Context, store *gitstore.Store, run, change string, prs []int) error {
	if err := errors.Join(checkName("run", run), checkText("change", change)); err != nil {
		return err
	}

	data, err := json.Marshal(alsoTo{AlsoTo: prs})
	if err != nil {
		return fmt.Errorf("encode also_to: %w", err)
	}

	_, err = store.Write(ctx, "also_to "+run+" "+change, gitstore.Update{
		Ref: AlsoToRef(run, change), Any: true, Files: map[string][]byte{alsoToFile: append(data, '\n')},
	})

	return err
}

// Collect writes every slice of a suite in the run into out, for a
// whole-suite check to read.
func Collect(ctx context.Context, store *gitstore.Store, id ID, suite, out string) (int, error) {
	if err := errors.Join(id.Check(), checkName("suite", suite)); err != nil {
		return 0, err
	}

	refs, err := store.List(ctx, id.Prefix()+suite+"/")
	if err != nil {
		return 0, err
	}

	n := 0

	for name, ref := range refs {
		_, slice, ok, err := id.sliceOf(name)
		if err != nil {
			return n, err
		}

		if !ok || checkSlice(slice) != nil {
			continue
		}

		// failure files are for people, not the check: never fetched
		files, err := store.FilesBut(ctx, ref.Commit, filesDir)
		if err != nil {
			return n, err
		}

		for p, data := range files {
			if checkSlice(p) != nil {
				continue
			}

			dst := filepath.Join(out, filepath.FromSlash(slice), filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return n, fmt.Errorf("collect: %w", err)
			}

			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return n, fmt.Errorf("collect: %w", err)
			}
		}

		n++
	}

	return n, nil
}

// Recordings returns what each slice of the run recorded, keyed
// "<suite>/<slice>.txt" as the lists hold them.
func Recordings(ctx context.Context, store *gitstore.Store, id ID) (map[string][]byte, error) {
	if err := id.Check(); err != nil {
		return nil, err
	}

	refs, err := store.List(ctx, id.Prefix())
	if err != nil {
		return nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, recordingFile)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}

	for name, ref := range refs {
		rec, ok := data[ref.Commit]
		if !ok {
			continue
		}

		suite, slice, isState, err := id.sliceOf(name)
		if err != nil {
			return nil, err
		}

		if isState {
			out[suite+"/"+slice+".txt"] = rec
		}
	}

	return out, nil
}

// SliceStates reads every slice state the run's refs hold, keyed "<suite>/<slice>", planned or
// not: what a slice's own steps last wrote, never the plan's view of it.
func SliceStates(ctx context.Context, store *gitstore.Store, id ID) (map[string]State, error) {
	if err := id.Check(); err != nil {
		return nil, err
	}

	refs, err := store.List(ctx, id.Prefix())
	if err != nil {
		return nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return nil, err
	}

	out := map[string]State{}

	for name, ref := range refs {
		raw, ok := data[ref.Commit]
		if !ok {
			continue
		}

		suite, slice, isState, err := id.sliceOf(name)
		if err != nil {
			return nil, err
		}

		if !isState {
			continue
		}

		var sf StateFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrID, name, err)
		}

		out[suite+"/"+slice] = sf.State
	}

	return out, nil
}

// Keep says which runs and lists the sweep keeps.
type Keep struct {
	// Runs is the default for every run name, PerRun overrides it by name.
	Runs   KeepRuns
	PerRun map[string]KeepRuns
	Lists  KeepLists
}

// KeepRuns keeps a run among the newest Last of its name or younger than Age; a zero value
// keeps nothing by its rule, and with both zero every run stays.
type KeepRuns struct {
	Last int
	Age  time.Duration
}

// KeepLists keeps lists the same way and, when Mainline names the default branch's first
// parents (newest first), those of its first Commits whatever their age, and none after.
type KeepLists struct {
	Last int
	Age  time.Duration

	Commits  int
	Mainline []string
}

func KeepFrom(cfg *config.Config) Keep {
	conv := func(r config.Retention) KeepRuns {
		age, _ := r.For.Duration()

		return KeepRuns{Last: r.Last, Age: age}
	}

	lists := conv(cfg.Keep.Lists.Retention)
	k := Keep{Runs: conv(cfg.Keep.Runs), PerRun: map[string]KeepRuns{}, Lists: KeepLists{Last: lists.Last, Age: lists.Age, Commits: cfg.ListsCommits()}}

	for _, name := range cfg.RunNames() {
		if cfg.Runs[name].Keep != nil {
			k.PerRun[name] = conv(cfg.KeepRuns(name))
		}
	}

	return k
}

func (k KeepRuns) keeps(rank int, age time.Duration) bool {
	if k.Last == 0 && k.Age == 0 {
		return true
	}

	return (k.Last > 0 && rank < k.Last) || (k.Age > 0 && age <= k.Age)
}

type runKey struct{ run, change, commit string }

func runOf(name string) (runKey, bool) {
	for _, kind := range []string{"runs/", "plans/"} {
		rest, ok := strings.CutPrefix(name, refRoot+kind)
		if !ok {
			continue
		}

		parts := strings.SplitN(rest, "/", 4)
		if len(parts) < 3 {
			return runKey{}, false
		}

		return runKey{parts[0], parts[1], parts[2]}, true
	}

	return runKey{}, false
}

type sweptRun struct {
	key          runKey
	refs         []gitstore.Ref
	plan, newest time.Time
}

func (r *sweptRun) when() time.Time {
	if !r.plan.IsZero() {
		return r.plan
	}

	return r.newest
}

// Sweep removes what keep no longer holds: whole runs (each one atomic delete), orphaned also_to
// refs and old lists, never protect's or a branch's newest; a run changed meanwhile waits.
func Sweep(ctx context.Context, store *gitstore.Store, now time.Time, keep Keep, protect []string) (int, error) {
	refs, err := store.List(ctx, refRoot+"runs/", refRoot+"plans/", refRoot+"also-to/", refRoot+"lists/", attached.Prefix)
	if err != nil {
		return 0, err
	}

	runs, lists, also := sortRefs(refs)
	byCommit := attachedRefs(refs)
	doomed, kept := sweepRuns(runs, keep, now)

	newest, err := branchesNewest(ctx, store, lists, byCommit)
	if err != nil {
		return 0, err
	}

	protect = append(slices.Clone(protect), newest...)

	removed := 0

	for _, r := range doomed {
		n, err := deleteRun(ctx, store, r.refs)
		removed += n

		if err != nil {
			return removed, err
		}
	}

	n, err := deleteRefs(ctx, store, "sweep also_to", orphanAlso(also, kept))
	if err != nil {
		return removed, err
	}

	removed += n

	// lists go in batches, being independent; a commit's attached ref goes with its lists, or
	// alone once they are gone
	old := sweepLists(lists, keep.Lists, now, protect)
	has := map[string]bool{}

	for _, ref := range lists {
		has[strings.TrimPrefix(ref.Name, refRoot+"lists/")] = true
	}

	for _, ref := range slices.Clone(old) {
		if a, ok := byCommit[strings.TrimPrefix(ref.Name, refRoot+"lists/")]; ok {
			old = append(old, a)
		}
	}

	var orphans []gitstore.Ref

	for commit, a := range byCommit {
		if !has[commit] {
			orphans = append(orphans, a)
		}
	}

	old = append(old, sweepLists(orphans, keep.Lists, now, protect)...)

	for start := 0; start < len(old); start += listBatch {
		n, err := deleteRefs(ctx, store, "sweep lists", old[start:min(start+listBatch, len(old))])
		if err != nil {
			return removed, err
		}

		removed += n
	}

	return removed, nil
}

const listBatch = 300

func orphanAlso(also []gitstore.Ref, kept map[string]bool) []gitstore.Ref {
	var out []gitstore.Ref

	for _, ref := range also {
		rest := strings.TrimPrefix(ref.Name, refRoot+"also-to/")
		if run, change, ok := strings.Cut(rest, "/"); ok && !kept[run+"/"+change] {
			out = append(out, ref)
		}
	}

	return out
}

var prChange = regexp.MustCompile(`^pr-[0-9]+$`)

// branchesNewest is the commit of each branch's newest lists still there: a quiet branch keeps
// them however old, so its next run plans from them rather than from nothing.
func branchesNewest(ctx context.Context, store *gitstore.Store, lists []gitstore.Ref, attachedBy map[string]gitstore.Ref) ([]string, error) {
	type found struct {
		commit string
		when   time.Time
	}

	best := map[string]found{}

	note := func(commit string, ref gitstore.Ref, files map[string][]byte) {
		for file := range files {
			parts := strings.Split(strings.TrimPrefix(file, attached.Dir), "/")
			if len(parts) != 3 {
				continue
			}

			change, err := url.PathUnescape(parts[1])
			if err != nil || prChange.MatchString(change) {
				continue
			}

			if b, ok := best[change]; !ok || ref.Time.After(b.when) {
				best[change] = found{commit, ref.Time}
			}
		}
	}

	for _, ref := range lists {
		commit := strings.TrimPrefix(ref.Name, refRoot+"lists/")

		// the runs attached to it, in the lists ref and, for older lists, in
		// a ref of their own
		files, err := store.Under(ctx, ref.Commit, attached.Dir)
		if err != nil {
			return nil, err
		}

		note(commit, ref, files)

		if a, ok := attachedBy[commit]; ok {
			if files, err = store.Files(ctx, a.Commit); err != nil {
				return nil, err
			}

			note(commit, a, files)
		}
	}

	out := make([]string, 0, len(best))
	for _, b := range best {
		out = append(out, b.commit)
	}

	slices.Sort(out)

	return out, nil
}

func attachedRefs(refs map[string]gitstore.Ref) map[string]gitstore.Ref {
	out := map[string]gitstore.Ref{}

	for name, ref := range refs {
		if commit, ok := strings.CutPrefix(name, attached.Prefix); ok {
			out[commit] = ref
		}
	}

	return out
}

func sortRefs(refs map[string]gitstore.Ref) (map[runKey]*sweptRun, []gitstore.Ref, []gitstore.Ref) {
	runs := map[runKey]*sweptRun{}

	var lists, also []gitstore.Ref

	for name, ref := range refs {
		switch {
		case strings.HasPrefix(name, refRoot+"lists/"):
			lists = append(lists, ref)
		case strings.HasPrefix(name, refRoot+"also-to/"):
			also = append(also, ref)
		default:
			k, ok := runOf(name)
			if !ok {
				continue
			}

			r := runs[k]
			if r == nil {
				r = &sweptRun{key: k}
				runs[k] = r
			}

			r.refs = append(r.refs, ref)

			if strings.HasPrefix(name, refRoot+"plans/") {
				r.plan = ref.Time
			}

			if ref.Time.After(r.newest) {
				r.newest = ref.Time
			}
		}
	}

	return runs, lists, also
}

func sweepRuns(runs map[runKey]*sweptRun, keep Keep, now time.Time) ([]*sweptRun, map[string]bool) {
	byName := map[string][]*sweptRun{}
	for _, r := range runs {
		byName[r.key.run] = append(byName[r.key.run], r)
	}

	var doomed []*sweptRun

	kept := map[string]bool{}

	for name, rs := range byName {
		rule := keep.Runs
		if k, ok := keep.PerRun[name]; ok {
			rule = k
		}

		slices.SortFunc(rs, func(a, b *sweptRun) int { return b.when().Compare(a.when()) })

		for rank, r := range rs {
			if rule.keeps(rank, now.Sub(r.when())) {
				kept[r.key.run+"/"+r.key.change] = true

				continue
			}

			doomed = append(doomed, r)
		}
	}

	return doomed, kept
}

func sweepLists(lists []gitstore.Ref, keep KeepLists, now time.Time, protect []string) []gitstore.Ref {
	slices.SortFunc(lists, func(a, b gitstore.Ref) int { return b.Time.Compare(a.Time) })

	rule := KeepRuns{Last: keep.Last, Age: keep.Age}

	// the default branch's last commits keep theirs; its older ones don't,
	// however young their lists are
	recent := map[string]bool{}
	older := map[string]bool{}

	for i, c := range keep.Mainline {
		if i < keep.Commits {
			recent[c] = true
		} else {
			older[c] = true
		}
	}

	old := make([]gitstore.Ref, 0, len(lists))

	for rank, ref := range lists {
		commit := ref.Name[strings.LastIndex(ref.Name, "/")+1:]

		switch {
		case slices.Contains(protect, commit), recent[commit]:
			continue
		case older[commit]:
		case rule.keeps(rank, now.Sub(ref.Time)):
			continue
		}

		old = append(old, ref)
	}

	return old
}

// deleteRun removes a run's refs in one atomic push, or in guarded chunks for a large run with
// its plan in the last, so a partial delete still leaves a run the next sweep finds.
func deleteRun(ctx context.Context, store *gitstore.Store, refs []gitstore.Ref) (int, error) {
	refs = slices.Clone(refs)
	slices.SortStableFunc(refs, func(a, b gitstore.Ref) int {
		return cmpBool(strings.HasPrefix(a.Name, refRoot+"plans/"), strings.HasPrefix(b.Name, refRoot+"plans/"))
	})

	removed := 0

	for start := 0; start < len(refs); start += listBatch {
		n, err := deleteRefs(ctx, store, "sweep run", refs[start:min(start+listBatch, len(refs))])
		removed += n

		if err != nil || n == 0 {
			return removed, err
		}
	}

	return removed, nil
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// deleteRefs removes refs that still point where they were listed, in one
// atomic push; when one moved meanwhile, none go.
func deleteRefs(ctx context.Context, store *gitstore.Store, message string, refs []gitstore.Ref) (int, error) {
	if len(refs) == 0 {
		return 0, nil
	}

	ups := make([]gitstore.Update, 0, len(refs))
	for _, ref := range refs {
		ups = append(ups, gitstore.Update{Ref: ref.Name, Old: ref.Commit, Delete: true})
	}

	_, err := store.Write(ctx, message, ups...)

	switch {
	case errors.Is(err, gitstore.ErrStale):
		return 0, nil
	case err != nil:
		return 0, err
	}

	return len(refs), nil
}

func StateFiles(ctx context.Context, store *gitstore.Store, id ID, suite string) (map[string]StateFile, error) {
	if err := errors.Join(id.Check(), checkName("suite", suite)); err != nil {
		return nil, err
	}

	refs, err := store.List(ctx, id.Prefix()+suite+"/")
	if err != nil {
		return nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return nil, err
	}

	out := map[string]StateFile{}

	for name, ref := range refs {
		_, slice, ok, err := id.sliceOf(name)
		if err != nil {
			return nil, err
		}

		raw, has := data[ref.Commit]
		if !ok || !has {
			continue
		}

		var sf StateFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			sf = StateFile{State: Errored, Failures: []Failure{{Test: "(unreadable state)", Tail: err.Error()}}}
		}

		out[slice] = sf
	}

	return out, nil
}

// Seconds reads how long each slice of a suite took in a run, by slice
// name, as the batches' journals timed it.
func Seconds(ctx context.Context, store *gitstore.Store, id ID, suite string) (map[string]float64, error) {
	if err := errors.Join(id.Check(), checkName("suite", suite)); err != nil {
		return nil, err
	}

	refs, err := store.List(ctx, id.Prefix()+suite+"/")
	if err != nil {
		return nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return nil, err
	}

	out := map[string]float64{}

	for name, ref := range refs {
		_, slice, ok, err := id.sliceOf(name)
		if err != nil {
			return nil, err
		}

		var sf StateFile
		if raw, has := data[ref.Commit]; !ok || !has || json.Unmarshal(raw, &sf) != nil || sf.Seconds <= 0 {
			continue
		}

		out[slice] = sf.Seconds
	}

	return out, nil
}

// cores is the cores the slice kept busy on average: its CPU seconds over the time they were read over,
// else, in an older state, over its seconds; 0 unknown.
func (sf StateFile) cores() float64 {
	switch {
	case sf.CPUSeconds <= 0:
		return 0
	case sf.CPUWall > 0:
		return sf.CPUSeconds / sf.CPUWall
	case sf.Seconds > 0:
		return sf.CPUSeconds / sf.Seconds
	default:
		return 0
	}
}
