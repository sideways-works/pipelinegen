// Package config reads pipelinegen.yaml, the one file a repository writes to
// tell pipelinegen about its suites.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/gitssh"
)

// FileName is the config file's name at the repository's root.
const FileName = "pipelinegen.yaml"

const defaultBranch = "main"

// Config is the whole of pipelinegen.yaml.
type Config struct {
	// Warnings are what loading met and let pass: a key going away or, in a
	// config read as history, anything this pipelinegen no longer takes.
	Warnings []string `yaml:"-"`
	// OldSpellings name keys in an older spelling this pipelinegen still
	// reads; only check and plan say so.
	OldSpellings []string `yaml:"-"`
	// Notices are what only pipelinegen check says: a key nothing reads any
	// more.
	Notices []string `yaml:"-"`

	Pipeline     string           `yaml:"pipeline"`
	Limits       Limits           `yaml:"limits"`
	Bazel        Bazel            `yaml:"bazel"`
	BazelDiff    BazelDiff        `yaml:"bazel_diff"`
	Suites       map[string]Suite `yaml:"suites"`
	EverythingOn []string         `yaml:"everything_on"`
	GitHub       GitHub           `yaml:"github"`
	Git          Git              `yaml:"git"`
	Concourse    Concourse        `yaml:"concourse"`
	// Packages are what every suite's steps need installed.
	Packages Packages       `yaml:"packages,omitempty"`
	Images   Images         `yaml:"images,omitempty"`
	Learn    Learn          `yaml:"learn"`
	Keep     Keep           `yaml:"keep"`
	Writes   Writes         `yaml:"writes"`
	Store    Store          `yaml:"store"`
	Runs     map[string]Run `yaml:"runs"`
	Untested []Untested     `yaml:"untested"`
	// Macros declares what a macro of another repository reads in the
	// package that calls it, by "<.bzl label>%<name>": the check can't see
	// inside such a macro.
	Macros      map[string]Macro `yaml:"macros"`
	Uncovered   string           `yaml:"uncovered"`
	Follow      string           `yaml:"follow"`
	FollowDepth int              `yaml:"follow_depth"`
	// Narrowing is how much a change's selection is narrowed: 1 narrows
	// most, 5 least (default 3); suites may override it.
	Narrowing      int `yaml:"narrowing"`
	NarrowingAlarm int `yaml:"narrowing_alarm"`
	// Narrowings set, by name, the level a plugin's narrowing or the core's
	// is switched off at, in place of the level its declarer gives.
	Narrowings map[string]NarrowingLevel `yaml:"narrowings"`
	// RebuildCheck is what a difference between the cached graph and the
	// nightly's cold rebuild does to the run: error (the default) turns it
	// red, warning only warns.
	RebuildCheck string `yaml:"rebuild_check"`
	// ExplainLimit is how many selected slices a plan asks bazel-diff to
	// explain (default DefaultExplainLimit; 0 none). A pointer keeps 0 apart
	// from left out.
	ExplainLimit *int             `yaml:"explain_limit"`
	SampleOn     []string         `yaml:"sample_on"`
	Plugins      []Plugin         `yaml:"plugins"`
	Connectors   []Connector      `yaml:"connectors"`
	Generated    []map[string]any `yaml:"generated"`
}

// Learn is the retired built-in binding's section: nothing reads it, and
// a config that sets it gets a warning.
type Learn struct {
	Modules []string    `yaml:"modules"`
	Pure    []string    `yaml:"pure"`
	Files   []FileUnits `yaml:"files"`
}

// FileUnits are plain files bound as units, normalised alike.
type FileUnits struct {
	Paths     []string `yaml:"paths"`
	Normalize string   `yaml:"normalize"`
}

// Plugin is one plugin executable, pinned by a release and its checksum or
// by a commit to build.
type Plugin struct {
	Name           string         `yaml:"name"`
	Kind           string         `yaml:"kind"`
	Repository     string         `yaml:"repository"`
	Release        *Release       `yaml:"release"`
	Commit         string         `yaml:"commit"`
	Include        []string       `yaml:"include"`
	Exclude        []string       `yaml:"exclude"`
	Config         map[string]any `yaml:"config"`
	Fill           *bool          `yaml:"fill"`
	CollectSeconds int            `yaml:"collect_seconds"`
}

// Fills tells whether the local commands fill the plugin's caches.
func (p Plugin) Fills() bool { return p.Fill == nil || *p.Fill }

// DefaultCollectSeconds bounds a correlator's collect call unless its
// entry says otherwise: a collector that hangs must not hold its batch.
const DefaultCollectSeconds = 10

// MaxCollectSeconds is how long the collect calls of a suite's correlators
// may take together: a mark waits for each in turn, and the playwright
// driver's hook (marks.mjs) waits 120 seconds for a mark at most.
const MaxCollectSeconds = 90

// CollectLimit is how long each of the plugin's collect calls may take.
func (p Plugin) CollectLimit() time.Duration {
	if p.CollectSeconds > 0 {
		return time.Duration(p.CollectSeconds) * time.Second
	}

	return DefaultCollectSeconds * time.Second
}

// The kinds of plugin.
const (
	KindCorrelator = "correlator"
	KindSBOM       = "sbom"
	KindRunner     = "runner"
)

// Runner is the configured runner plugin; false without one.
func (c *Config) Runner() (Plugin, bool) {
	for _, p := range c.Plugins {
		if p.Kind == KindRunner {
			return p, true
		}
	}

	return Plugin{}, false
}

// Keep says which runs and lists the sweep keeps.
type Keep struct {
	Runs Retention `yaml:"runs"`
	// Lists keeps the lists attached to commits; default 90 days. The
	// newest lists of each branch are kept whatever this says.
	Lists ListsRetention `yaml:"lists"`
	Cache Size           `yaml:"cache"`
	// Files caps what a failed slice's failure files keep in the store.
	Files KeepFiles `yaml:"files"`
}

// KeepFiles caps the failure files a run keeps: each slice's, and the
// whole run's. 0 keeps none.
type KeepFiles struct {
	Slice Size `yaml:"slice"`
	Run   Size `yaml:"run"`
}

// The defaults of keep.files.slice and keep.files.run.
const (
	DefaultFilesSlice = Size("2MB")
	DefaultFilesRun   = Size("50MB")
)

// maxFilesSlice is GitHub's largest file: a slice's cap above it would let
// one file fail every push of the run's records.
const maxFilesSlice = 100 << 20

// FilesCaps are keep.files.slice and keep.files.run in bytes, with their
// defaults.
func (c *Config) FilesCaps() (slice, run int64) {
	return c.Keep.Files.Slice.or(DefaultFilesSlice), c.Keep.Files.Run.or(DefaultFilesRun)
}

func (s Size) or(def Size) int64 {
	if n, ok := s.Bytes(); ok {
		return n
	}

	n, _ := def.Bytes()

	return n
}

// DefaultCacheBudget is keep.cache's default: a worker holds one cache per
// planning job, and a repository has a few.
const DefaultCacheBudget = Size("8GB")

// Size is an amount of disk: a whole number with B, KB, MB, GB or TB
// (powers of 1024), or 0.
type Size string

// Bytes is the size in bytes; false when it is empty or malformed.
func (s Size) Bytes() (int64, bool) {
	if s == "0" {
		return 0, true
	}

	m := sizeRE.FindStringSubmatch(string(s))
	if m == nil {
		return 0, false
	}

	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}

	shift := map[string]uint{"B": 0, "KB": 10, "MB": 20, "GB": 30, "TB": 40}[m[2]]

	return n << shift, true
}

var sizeRE = regexp.MustCompile(`^([0-9]+)(B|KB|MB|GB|TB)$`)

// CacheBudget is keep.cache in bytes, with its default; 0 is no cap.
func (c *Config) CacheBudget() int64 {
	if n, ok := c.Keep.Cache.Bytes(); ok {
		return n
	}

	n, _ := DefaultCacheBudget.Bytes()

	return n
}

// ListsRetention keeps lists as a Retention does, and besides: those of
// the default branch's last Commits first-parent commits, whatever their
// age, and none of its commits before those.
type ListsRetention struct {
	Retention `yaml:",inline"`

	Commits int `yaml:"commits"`
}

// DefaultListsCommits is how many of the default branch's last commits
// keep their lists: a pull request not rebased for a while still finds its
// base's.
const DefaultListsCommits = 200

// ListsCommits is keep.lists.commits with its default.
func (c *Config) ListsCommits() int {
	if c.Keep.Lists.Commits > 0 {
		return c.Keep.Lists.Commits
	}

	return DefaultListsCommits
}

// Retention keeps the last N, or those of a period; with both, whatever
// either keeps.
type Retention struct {
	Last int    `yaml:"last"`
	For  Period `yaml:"for"`
}

// Writes is how a failed write to the store goes again: one that failed in
// passing until its deadline, one the remote refused by a rule of its own
// only a few times. A write whose ref moved under it reads again instead.
type Writes struct {
	// Retries is how often a write that failed in passing goes again at
	// least, however long that takes (default DefaultRetries; 0 tries
	// once). A pointer keeps 0 apart from left out.
	Retries *int `yaml:"retries"`
	// Backoff is the wait before the first retry (default
	// DefaultBackoff); each later one doubles it up to 30 seconds, cut to
	// a random part of it so writers that met don't meet again in step.
	Backoff Period `yaml:"backoff"`
	// Deadline is how long after its first try a write that failed in
	// passing keeps going again once Retries are spent (default
	// DefaultWriteDeadline, or half of limits.steps when that is shorter).
	// It must be shorter than limits.steps.
	Deadline Period `yaml:"deadline"`
}

// The defaults of writes.backoff and writes.deadline.
const (
	DefaultBackoff       = time.Second
	DefaultWriteDeadline = 10 * time.Minute
)

// Store is where pipelinegen keeps its records in the repository.
type Store struct {
	Backend string `yaml:"backend"`
	// Branch names the branch (default DefaultStoreBranch). An older
	// pipelinegen kept the leases of its locks on the branch of the same
	// name with "-locks" added, which the plan step sweeps.
	Branch   string `yaml:"branch"`
	Deadline Period `yaml:"deadline"`
}

// The store's backends and its default branch.
const (
	StoreRefs          = "refs"
	StoreBranch        = "branch"
	DefaultStoreBranch = "pipelinegen"
	defaultDeadline    = 10 * time.Minute
)

// OnBranch tells whether the records live on a branch.
func (c *Config) OnBranch() bool { return c.Store.Backend == StoreBranch }

// StoreBranchName is store.branch with its default.
func (c *Config) StoreBranchName() string {
	if c.Store.Branch == "" {
		return DefaultStoreBranch
	}

	return c.Store.Branch
}

// LocksBranchName is the branch an older pipelinegen kept the leases of its
// locks on.
func (c *Config) LocksBranchName() string { return c.StoreBranchName() + "-locks" }

// StoreDeadline is store.deadline with its default.
func (c *Config) StoreDeadline() time.Duration {
	if d, ok := c.Store.Deadline.Duration(); ok && c.Store.Deadline != "" {
		return d
	}

	return defaultDeadline
}

// DefaultRetries and MaxRetries bound writes.retries.
const (
	DefaultRetries = 10
	MaxRetries     = 50
)

// DefaultExplainLimit is explain_limit's default.
const DefaultExplainLimit = 25

// ExplainLimitOrDefault is explain_limit with its default, as planning's
// affected.Options takes it: positive, the limit; negative, none.
func (c *Config) ExplainLimitOrDefault() int {
	switch {
	case c.ExplainLimit == nil:
		return DefaultExplainLimit
	case *c.ExplainLimit == 0:
		return -1
	}

	return *c.ExplainLimit
}

// WriteRetries is writes.retries with its default.
func (c *Config) WriteRetries() int {
	if c.Writes.Retries == nil {
		return DefaultRetries
	}

	return *c.Writes.Retries
}

// WriteBackoff is writes.backoff with its default.
func (c *Config) WriteBackoff() time.Duration {
	if d, ok := c.Writes.Backoff.Duration(); ok && d > 0 {
		return d
	}

	return DefaultBackoff
}

// WriteDeadline is writes.deadline with its default: DefaultWriteDeadline,
// or half of limits.steps when that is shorter, so a step that writes
// keeps time for its other work.
func (c *Config) WriteDeadline() time.Duration {
	if d, ok := c.Writes.Deadline.Duration(); ok && d > 0 {
		return d
	}

	return min(DefaultWriteDeadline, time.Duration(c.Limits.StepMinutes())*time.Minute/2)
}

// Period is a length of time written with a unit: 30s, 5m, 12h, 5d, 2w.
type Period string

// Duration is the period's length; false when it is empty or malformed.
func (p Period) Duration() (time.Duration, bool) {
	m := periodRE.FindStringSubmatch(string(p))
	if m == nil {
		return 0, false
	}

	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}

	unit := map[string]time.Duration{
		"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
	}[m[2]]

	return time.Duration(n) * unit, true
}

var periodRE = regexp.MustCompile(`^([0-9]+)([smhdw])$`)

func (r Retention) valid() bool {
	if r.Last < 0 {
		return false
	}

	if r.For != "" {
		_, ok := r.For.Duration()

		return ok
	}

	return r.Last > 0
}

// Untested is a group of files no test covers, and why.
type Untested struct {
	Paths  []string `yaml:"paths"`
	Reason string   `yaml:"reason"`
}

// Macro is what a macro of another repository reads: glob patterns in the
// package that calls it, and why it is declared.
type Macro struct {
	Reads  []string `yaml:"reads"`
	Reason string   `yaml:"reason"`
}

// MacroKey splits a macros key, "@repo//pkg:file.bzl%name", into the .bzl
// file's label and the macro's name. ok is false for a key of another form
// or a .bzl file of this repository, whose macros the check reads itself.
func MacroKey(key string) (module, name string, ok bool) {
	module, name, found := strings.Cut(key, "%")
	if !found || name == "" || strings.Contains(name, "%") ||
		!strings.HasPrefix(module, "@") || strings.HasPrefix(module, "@//") || strings.HasPrefix(module, "@@//") ||
		!strings.Contains(module, "//") || !strings.HasSuffix(module, ".bzl") {
		return "", "", false
	}

	return module, name, true
}

// NarrowingLevel is the level the config switches one narrowing off at, and
// why.
type NarrowingLevel struct {
	OffAt int    `yaml:"off_at"`
	Note  string `yaml:"note"`
}

// The narrowing levels: 1 narrows every change most, MaxNarrowing least.
const (
	DefaultNarrowing = 3
	MaxNarrowing     = 5
)

// NarrowingFor is the level a suite is narrowed at.
func (c *Config) NarrowingFor(suite string) int {
	if n := c.Suites[suite].Narrowing; n > 0 {
		return n
	}

	return c.Narrowing
}

// The values of Suite.Bind.
const (
	BindGraph    = "graph"
	BindRecorded = "recorded"
)

// The values of Suite.EmptyRecordings.
const (
	EmptyAllow  = "allow"
	EmptyRefuse = "refuse"
)

// Records tells whether run records coverage for suite: only recorded
// suites do, in runs that record.
func (c *Config) Records(run, suite string) bool {
	return c.Runs[run].Record && c.Suites[suite].Bind == BindRecorded
}

// The values of Follow.
const (
	FollowExecuted   = "executed"
	FollowDependents = "dependents"
)

// The values of RebuildCheck.
const (
	RebuildError   = "error"
	RebuildWarning = "warning"
)

// The values of Uncovered.
const (
	UncoveredFail       = "fail"
	UncoveredEverything = "everything"
)

// Bazel says how to run Bazel in the repository.
type Bazel struct {
	Workspace    string   `yaml:"workspace"`
	Bazelisk     string   `yaml:"bazelisk"`
	StartupFlags []string `yaml:"startup_flags"`
}

// BazelDiff names a bazel-diff release and its checksum.
type BazelDiff struct {
	Version string `json:"version" yaml:"version"`

	SHA256 Checksums `json:"sha256" yaml:"sha256"`

	URL string `json:"url" yaml:"url"`
}

// Suite is one kind of check with one way to run it.
type Suite struct {
	Tag string `yaml:"tag"`
	// Tests are the patterns of the suite's test files: every matching file
	// is a slice of its own. A suite has tests or a tag, never both.
	Tests   []string `yaml:"tests"`
	Exclude []string `yaml:"exclude"`
	// Helpers are patterns of matching files that hold no test of their
	// own (shared helpers): inputs of the slices that use them, never
	// slices. Go test files are told apart by their test names instead.
	Helpers   []string    `yaml:"helpers"`
	Owner     string      `yaml:"owner"`
	ExtraDeps []ExtraDeps `yaml:"extra_deps"`
	// TextInputs are files some test files read as text: a change to their
	// comments or layout is one those tests can observe, so no narrowing
	// drops it.
	TextInputs []TextInputs `yaml:"text_inputs"`
	Durations  string       `yaml:"durations"`
	Group      string       `yaml:"group"`
	Prepare    string       `yaml:"prepare"`
	RunSlice   string       `yaml:"run_slice"`
	Steps      []Step       `yaml:"steps,omitempty"`
	RunOn      RunOn        `yaml:"run_on"`
	Image      string       `yaml:"image,omitempty"`
	// Packages are what the suite's steps need besides the top level's.
	Packages Packages `yaml:"packages,omitempty"`
	// Params go to the suite's prepare, batch and whole-suite tasks: plain
	// values, never secrets.
	Params      map[string]string `yaml:"params"`
	Secrets     Secrets           `yaml:"secrets"`
	Correlators []string          `yaml:"correlators"`
	WholeSuite  []WholeSuiteCheck `yaml:"whole_suite"`
	Follow      string            `yaml:"follow"`
	FollowDepth int               `yaml:"follow_depth"`
	Narrowing   int               `yaml:"narrowing"`
	Sample      int               `yaml:"sample"`
	SampleOn    []string          `yaml:"sample_on"`
	Bind        string            `yaml:"bind"`
	Parallel    int               `yaml:"parallel"`
	// Batches is how many batches a job deals the suite's slices into
	// (default Parallel), every one run at once: more, smaller batches lose
	// less unflushed work when a worker goes away.
	Batches int `yaml:"batches"`
	// BatchMinutes, when set, deals the suite's selected slices into
	// batches of about this many minutes each by their recorded durations,
	// so a run of everything gets many small batches and a change of a few
	// slices few; Batches is then the most it deals.
	BatchMinutes int  `yaml:"batch_minutes"`
	Privileged   bool `yaml:"privileged"`
	// Stack says the suite's batches run long-lived processes (servers,
	// workers) whose coverage only a correlator's collect can take: a
	// recording run of it refuses to start without one.
	Stack bool `yaml:"stack"`
	// EmptyRecordings says whether a recording in which the correlators saw
	// nothing may become an empty list (allow) or counts as one they
	// couldn't tell (refuse, the default): a correlator that wrongly answers
	// empty would bind its slice to nothing.
	EmptyRecordings string `yaml:"empty_recordings"`
	Lock            *Lock  `yaml:"lock"`
	Minutes         int    `yaml:"minutes"`
}

// ExtraDeps makes the test files matching Tests depend on Deps too.
type ExtraDeps struct {
	Tests []string `yaml:"tests"`
	Deps  []string `yaml:"deps"`
}

// TextInputs says the test files matching Tests (every test file of the
// suite when none is given) read the files matching Paths as text.
type TextInputs struct {
	Tests []string `yaml:"tests"`
	Paths []string `yaml:"paths"`
}

// ReadsText tells whether the suite declares that the test file reads
// the file's text.
func (s Suite) ReadsText(test, file string) bool {
	for _, ti := range s.TextInputs {
		if len(ti.Tests) > 0 && !slices.ContainsFunc(ti.Tests, func(p string) bool { return MatchGlob(p, test) }) {
			continue
		}

		if slices.ContainsFunc(ti.Paths, func(p string) bool { return MatchGlob(p, file) }) {
			return true
		}
	}

	return false
}

// Implicit tells whether the suite's slices are its test files: it names
// test patterns and no tag.
func (s Suite) Implicit() bool { return len(s.Tests) > 0 && s.Tag == "" }

// Lock is no longer read, so an older config still loads; a notice says so.
// Minutes alone is still read, as the old spelling of the suite's minutes.
type Lock struct {
	Name       string `yaml:"name"`
	Repository string `yaml:"repository"`
	Minutes    int    `yaml:"minutes"`
	Slots      int    `yaml:"slots"`
	Per        string `yaml:"per"`
	Weight     int    `yaml:"weight"`
}

// What holds a slot of a suite's lock.
const (
	LockPerBuild = "build"
	LockPerBatch = "batch"
)

// PerBatch tells whether each batch takes the lock, not the whole build.
func (l *Lock) PerBatch() bool { return l != nil && l.Per == LockPerBatch }

// RunOn says how builds of different changes share a suite's jobs.
type RunOn struct {
	SerialGroup string `yaml:"serial_group"`
	MaxInFlight int    `yaml:"max_in_flight"`
}

// ParallelOrDefault is how many batches a build of the suite is dealt at
// least, every one run at once.
func (s Suite) ParallelOrDefault() int {
	if s.Parallel > 0 {
		return s.Parallel
	}

	return DefaultParallel
}

// BatchCount is how many batches a job deals the suite's slices into:
// batches, else as many as run at once.
func (s Suite) BatchCount() int {
	if s.Batches > 0 {
		return s.Batches
	}

	return s.ParallelOrDefault()
}

// DefaultParallel is how many batches a build of a suite is dealt unless the
// suite says otherwise.
const DefaultParallel = 4

// WholeSuiteCheck runs only when every slice of its suite is selected, or
// when a change reaches one of the targets it names.
type WholeSuiteCheck struct {
	Name       string   `yaml:"name"`
	Task       string   `yaml:"task"`
	AlsoOn     []string `yaml:"also_on"`
	Privileged bool     `yaml:"privileged"`
}

var (
	suiteName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	tagName   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// ErrInvalid wraps every problem Validate finds.
var ErrInvalid = errors.New("invalid config")

// Load reads and checks the config file at p.
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p) //nolint:gosec // the path is the config the caller names
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return Parse(data)
}

// Parse reads and checks a config from its YAML text. Unknown keys are
// errors, so a typo never turns a setting off silently.
func Parse(data []byte) (*Config, error) {
	if retired := retiredPlacement(data); len(retired) > 0 {
		errs := make([]error, 0, len(retired))
		for _, r := range retired {
			errs = append(errs, fmt.Errorf("%w: %s", ErrInvalid, r))
		}

		return nil, errors.Join(errs...)
	}

	var c Config

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	c.defaults()

	if err := c.Validate(); err != nil {
		return nil, err
	}

	return &c, nil
}

// EnvConcourseURL holds Concourse's address when the config leaves
// concourse.url out: a host is the CI's, not the repository's. Only
// generating the pipeline needs it.
const EnvConcourseURL = "PIPELINEGEN_CONCOURSE_URL"

func (c *Config) learnGoingAway() {
	if len(c.Learn.Modules)+len(c.Learn.Pure)+len(c.Learn.Files) > 0 {
		c.Warnings = append(c.Warnings, "learn is no longer read and will be refused: recorded suites bind through sbom and correlator plugins; remove the key")
	}
}

// ErrUnreadable is a commit's config that can't say which suites and test
// files it had.
var ErrUnreadable = errors.New("config can't be read")

// ParseHistory reads a config as an earlier commit has it. History must
// always load: unknown, refused or invalid keys become Warnings, and only a
// config that can't say which suites and test files it had fails
// (ErrUnreadable).
func ParseHistory(data []byte) (*Config, error) {
	var c Config

	if err := yaml.Unmarshal(data, &c); err != nil {
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}

		c.Warnings = append(c.Warnings, "values of another type than this pipelinegen reads: "+strings.Join(te.Errors, "; "))
	}

	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)

	var probe Config
	if err := strict.Decode(&probe); err != nil && !strings.Contains(strings.Join(c.Warnings, ""), err.Error()) {
		c.Warnings = append(c.Warnings, "keys this pipelinegen doesn't read: "+err.Error())
	}

	c.defaults()

	if err := c.Validate(); err != nil {
		for line := range strings.SplitSeq(err.Error(), "\n") {
			c.Warnings = append(c.Warnings, line)
		}
	}

	if len(c.Suites) == 0 {
		return nil, fmt.Errorf("%w: it names no suites", ErrUnreadable)
	}

	for name, s := range c.Suites {
		if len(s.Tests) == 0 && s.Tag == "" {
			return nil, fmt.Errorf("%w: suite %s names no tests and no tag", ErrUnreadable, name)
		}
	}

	return &c, nil
}

func (c *Config) defaults() {
	if c.Concourse.URL == "" {
		c.Concourse.URL = os.Getenv(EnvConcourseURL)
	}

	if c.Bazel.Workspace == "" {
		c.Bazel.Workspace = "."
	}

	if c.Bazel.Bazelisk == "" {
		c.Bazel.Bazelisk = "bazelisk"
	}

	if bs := &c.Concourse.Planner.Bootstrap; (bs.Release != nil || bs.Commit != "") && bs.Bazelisk == (Download{}) {
		bs.Bazelisk = DefaultBazelisk
	}

	if c.GitHub.StatusPrefix == "" {
		c.GitHub.StatusPrefix = "pipelinegen"
	}

	if c.Uncovered == "" {
		c.Uncovered = UncoveredEverything
	}

	if c.Follow == "" {
		c.Follow = FollowExecuted
	}

	if c.Narrowing == 0 {
		c.Narrowing = DefaultNarrowing
	}

	if c.NarrowingAlarm == 0 {
		c.NarrowingAlarm = 2
	}

	if c.RebuildCheck == "" {
		c.RebuildCheck = RebuildError
	}

	// changes is pull_requests by another name: one setting from here on,
	// unless both are given, which validation refuses
	for name, r := range c.Runs {
		if r.Trigger.Changes != nil && r.Trigger.PullRequests == nil {
			r.Trigger.PullRequests, r.Trigger.Changes = r.Trigger.Changes, nil
			c.Runs[name] = r
		}
	}

	if c.Keep.Runs == (Retention{}) {
		c.Keep.Runs = Retention{For: "7d"}
	}

	if c.Keep.Lists.Retention == (Retention{}) {
		c.Keep.Lists.Retention = Retention{For: "90d"}
	}

	for name, s := range c.Suites {
		if s.Bind == "" {
			s.Bind = BindGraph
			c.Suites[name] = s
		}
	}

	if c.Concourse.Planner.Bootstrap.Repository == "" {
		c.Concourse.Planner.Bootstrap.Repository = "sideways-works/pipelinegen"
	}

	if c.Concourse.Branch == "" {
		c.Concourse.Branch = defaultBranch
	}

	if c.Concourse.Team == "" {
		c.Concourse.Team = "main"
	}
}

func (c *Config) validateKeep(bad func(string, ...any)) {
	if !c.Keep.Runs.valid() || !c.Keep.Lists.valid() {
		bad("keep: runs and lists each need last (a count) or for (12h, 5d, 2w)")
	}

	if c.Keep.Lists.Commits < 0 {
		bad("keep.lists.commits must not be negative")
	}

	if _, ok := c.Keep.Cache.Bytes(); c.Keep.Cache != "" && !ok {
		bad("keep.cache needs a size such as 8GB or 500MB, or 0 for no cap")
	}

	for _, k := range []struct {
		key string
		v   Size
	}{{"keep.files.slice", c.Keep.Files.Slice}, {"keep.files.run", c.Keep.Files.Run}} {
		if _, ok := k.v.Bytes(); k.v != "" && !ok {
			bad("%s needs a size such as 2MB or 500KB, or 0 to keep none", k.key)
		}
	}

	if n, _ := c.Keep.Files.Slice.Bytes(); n > maxFilesSlice {
		bad("keep.files.slice is %s, over 100MB, the largest file GitHub takes", c.Keep.Files.Slice)
	}
}

// Validate reports every problem in the config at once.
func (c *Config) Validate() error {
	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...)))
	}

	if c.Pipeline == "" {
		bad("pipeline is missing")
	}

	c.BazelDiff.validate(bad)

	if len(c.Suites) == 0 {
		bad("no suites")
	}

	c.validateSuites(bad)

	for _, f := range c.Learn.Files {
		if len(f.Paths) == 0 || (f.Normalize != "js" && f.Normalize != "css" && f.Normalize != "none") {
			bad("learn.files: every entry needs paths and normalize js, css or none")
		}
	}

	for name, su := range c.Suites {
		if su.Bind == BindRecorded && !c.bindsByPlugins() {
			bad("suite %s binds by recording, which needs sbom and correlator plugins to read its recordings (see the plugin proof's fixture)", name)
		}
	}

	for _, p := range c.EverythingOn {
		if err := checkGlob(p); err != nil {
			bad("everything_on %q: %v", p, err)
		}
	}

	c.validateChoices(bad)

	c.validateKeep(bad)
	c.validateNarrowing(bad)
	c.validateImages(bad)
	c.validatePackages(bad)
	c.validateWrites(bad)
	c.validatePlugins(bad)

	c.validateUntested(bad)
	c.validateMacros(bad)
	c.validateRuns(bad)
	c.validateConnectors(bad)
	c.validateSSH(bad)
	c.validateSuiteSecrets(bad)

	c.validateTaskSecrets(bad)

	c.validateCorrelators(bad)
	c.validateCollectLimits(bad)
	c.validateLimits(bad)
	c.learnGoingAway()
	c.oldMinutes()

	return errors.Join(errs...)
}

func (c *Config) validateUntested(bad func(string, ...any)) {
	for _, u := range c.Untested {
		if len(u.Paths) == 0 || u.Reason == "" {
			bad("every untested entry needs paths and a reason")
		}

		for _, p := range u.Paths {
			if err := checkGlob(p); err != nil {
				bad("untested %q: %v", p, err)
			}
		}
	}
}

func (c *Config) validateMacros(bad func(string, ...any)) {
	for _, key := range slices.Sorted(maps.Keys(c.Macros)) {
		m := c.Macros[key]

		if _, _, ok := MacroKey(key); !ok {
			bad("macros %q: the key is \"@repo//pkg:file.bzl%%name\", a macro of another repository (the check reads this repository's own)", key)
		}

		if len(m.Reads) == 0 || m.Reason == "" {
			bad("macros %q: needs reads and a reason", key)
		}

		for _, p := range m.Reads {
			if err := checkGlob(p); err != nil || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
				bad("macros %q: reads %q is no pattern within the calling package", key, p)
			}
		}
	}
}

// SuiteNames lists the suites in a stable order.
func (c *Config) SuiteNames() []string {
	names := make([]string, 0, len(c.Suites))
	for name := range c.Suites {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// SampleOnFor names the sample_on pattern, the suite's or the top
// level's, that a changed file matches.
func (c *Config) SampleOnFor(suite, file string) (string, bool) {
	for _, p := range slices.Concat(c.Suites[suite].SampleOn, c.SampleOn) {
		if MatchGlob(p, file) {
			return p, true
		}
	}

	return "", false
}

// Rebuilds tells whether run builds the graph cold to check the cached
// one: a run that selects everything and records, never one of pull
// requests, in a repository whose suites bind through plugins.
func (c *Config) Rebuilds(run string) bool {
	r := c.Runs[run]

	return r.Select == SelectAll && r.Record && r.Trigger.PullRequests == nil && c.bindsByPlugins()
}

func (c *Config) validateChoices(bad func(string, ...any)) {
	if c.Uncovered != UncoveredFail && c.Uncovered != UncoveredEverything {
		bad("uncovered must be fail or everything")
	}

	if c.RebuildCheck != RebuildError && c.RebuildCheck != RebuildWarning {
		bad("rebuild_check must be error or warning")
	}

	if !validFollow(c.Follow) || c.FollowDepth < 0 {
		bad("follow must be executed or dependents, follow_depth 0 or more")
	}
}

func (c *Config) bindsByPlugins() bool {
	return slices.ContainsFunc(c.Plugins, func(p Plugin) bool { return p.Kind == KindSBOM || p.Kind == KindCorrelator })
}

// MatchesSampleOn tells whether a changed file matches any suite's
// sample_on or the top level's.
func (c *Config) MatchesSampleOn(file string) bool {
	if _, ok := c.SampleOnFor("", file); ok {
		return true
	}

	for _, name := range c.SuiteNames() {
		if _, ok := c.SampleOnFor(name, file); ok {
			return true
		}
	}

	return false
}

// UntestedFor is the untested entry a file (a path relative to the
// repository's root) matches.
func (c *Config) UntestedFor(file string) (Untested, bool) {
	for _, u := range c.Untested {
		if slices.ContainsFunc(u.Paths, func(p string) bool { return MatchGlob(p, file) }) {
			return u, true
		}
	}

	return Untested{}, false
}

// MatchesEverythingOn tells whether a changed file (a path relative to the
// repository's root) is one whose change selects every slice, and which
// pattern matched.
func (c *Config) MatchesEverythingOn(file string) (string, bool) {
	if idx := slices.IndexFunc(c.EverythingOn, func(p string) bool { return MatchGlob(p, file) }); idx >= 0 {
		return c.EverythingOn[idx], true
	}

	return "", false
}

func (c *Config) validateSuites(bad func(string, ...any)) {
	tags := map[string]string{}

	for _, name := range c.SuiteNames() {
		s := c.Suites[name]

		if !suiteName.MatchString(name) {
			bad("suite %q: name must be lower case letters, digits and dashes", name)
		}

		validateSlices(name, s, tags, bad)
		validateSuiteSettings(name, s, bad)
		c.validateSteps(name, s, bad)
	}
}

func validateSlices(name string, s Suite, tags map[string]string, bad func(string, ...any)) {
	for _, p := range slices.Concat(s.Tests, s.Exclude, s.Helpers) {
		if err := checkGlob(p); err != nil {
			bad("suite %s: pattern %q: %v", name, p, err)
		}
	}

	switch {
	case s.Tag == "" && s.Implicit():
	case s.Tag == "":
		bad("suite %s: needs tests (its test files) or a tag (its targets)", name)
	case !tagName.MatchString(s.Tag):
		bad("suite %s: tag %q has characters a Bazel query can't match", name, s.Tag)
	case tags[s.Tag] != "":
		bad("suite %s: tag %s is suite %s's too", name, s.Tag, tags[s.Tag])
	default:
		tags[s.Tag] = name
	}

	if s.Group != "" {
		if _, err := regexp.Compile(s.Group); err != nil {
			bad("suite %s: group: %v", name, err)
		}
	}
}

func validateSuiteSettings(name string, s Suite, bad func(string, ...any)) {
	if s.RunSlice == "" && !s.HasSteps() {
		bad("suite %s: run_slice or steps is missing", name)
	}

	if s.BatchMinutes < 0 {
		bad("suite %s: batch_minutes must be at least 1", name)
	}

	if s.Parallel < 0 || s.Batches < 0 || s.RunOn.MaxInFlight < 0 || s.Sample < 0 || s.Narrowing < 0 || s.Narrowing > MaxNarrowing {
		bad("suite %s: negative numbers", name)
	}

	// fewer batches than run at once would leave a slot idle
	if s.Batches > 0 && s.Batches < s.ParallelOrDefault() {
		bad("suite %s: batches (%d) must be at least parallel (%d)", name, s.Batches, s.ParallelOrDefault())
	}

	if s.Bind != BindGraph && s.Bind != BindRecorded {
		bad("suite %s: bind must be graph or recorded", name)
	}

	if s.EmptyRecordings != "" && s.EmptyRecordings != EmptyAllow && s.EmptyRecordings != EmptyRefuse {
		bad("suite %s: empty_recordings must be allow or refuse", name)
	}

	for _, p := range s.SampleOn {
		if err := checkGlob(p); err != nil {
			bad("suite %s: sample_on %q: %v", name, p, err)
		}
	}

	validateTextInputs(name, s.TextInputs, bad)

	if (s.Follow != "" && !validFollow(s.Follow)) || s.FollowDepth < 0 {
		bad("suite %s: follow must be executed or dependents, follow_depth 0 or more", name)
	}

	for _, w := range s.WholeSuite {
		if w.Name == "" || w.Task == "" {
			bad("suite %s: a whole-suite check needs a name and a task", name)
		}
	}
}

func validateTextInputs(name string, tis []TextInputs, bad func(string, ...any)) {
	for _, ti := range tis {
		if len(ti.Paths) == 0 {
			bad("suite %s: a text_inputs entry needs paths", name)
		}

		for _, p := range slices.Concat(ti.Tests, ti.Paths) {
			if err := checkGlob(p); err != nil {
				bad("suite %s: text_inputs %q: %v", name, p, err)
			}
		}
	}
}

// ExcludesTarget tells whether a tagged target is left out of the suite by
// its exclude patterns.
func (s Suite) ExcludesTarget(label string) bool {
	pkg, _, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")

	return slices.ContainsFunc(s.Exclude, func(p string) bool {
		return MatchGlob(p, pkg) || MatchGlob(p, path.Join(pkg, "BUILD.bazel"))
	})
}

// OwnerOf fills the suite's owner template for a test file: {dir} is the
// file's directory, {file} its path, {base} its name, {stem} its name cut
// at the first dot (checkout.spec.ts is checkout). False without a
// template: the file's package owns it.
func (s Suite) OwnerOf(file string) (string, bool) {
	if s.Owner == "" {
		return "", false
	}

	dir, base := path.Split(file)
	stem, _, _ := strings.Cut(base, ".")

	return strings.NewReplacer(
		"{dir}", strings.TrimSuffix(dir, "/"),
		"{file}", file,
		"{base}", base,
		"{stem}", stem,
	).Replace(s.Owner), true
}

func (c *Config) validateWrites(bad func(string, ...any)) {
	if c.ExplainLimit != nil && *c.ExplainLimit < 0 {
		bad("explain_limit must be 0 or more")
	}

	if r := c.WriteRetries(); r < 0 || r > MaxRetries {
		bad("writes.retries must be 0 to %d", MaxRetries)
	}

	if _, ok := c.Writes.Backoff.Duration(); c.Writes.Backoff != "" && !ok {
		bad("writes.backoff must be a period such as 2s")
	}

	// a step that writes ends and says why before its limit ends it
	if d, ok := c.Writes.Deadline.Duration(); c.Writes.Deadline != "" && (!ok || d <= 0) {
		bad("writes.deadline must be a period such as 10m")
	} else if steps := time.Duration(c.Limits.StepMinutes()) * time.Minute; c.WriteDeadline() >= steps {
		bad("writes.deadline (%s) must be shorter than limits.steps (%d minutes), the time limit of the steps that write", c.WriteDeadline(), c.Limits.StepMinutes())
	}

	c.validateStore(bad)
}

// validateStore refuses a store that can't be; a branch store on a branch a
// run is triggered by would start the run again with every write.
func (c *Config) validateStore(bad func(string, ...any)) {
	if r := c.Git.Remote; r != "" && !validRemote(r) && !gitssh.IsSSH(r) {
		bad("git.remote must be an https, http, git or file URL without a user, password or query")
	}

	switch c.Store.Backend {
	case "", StoreRefs, StoreBranch:
	default:
		bad("store.backend must be %s or %s", StoreRefs, StoreBranch)
	}

	if b := c.Store.Branch; b != "" && !branchName.MatchString(b) {
		bad("store.branch must be a plain branch name")
	}

	if d, ok := c.Store.Deadline.Duration(); c.Store.Deadline != "" && (!ok || d <= 0) {
		bad("store.deadline must be a period such as 10m")
	}

	// only a branch store writes to its branches: with refs, a run of a
	// branch of the same name is no loop
	if !c.OnBranch() {
		return
	}

	for _, name := range c.RunNames() {
		for _, b := range c.runBranches(name) {
			if b == c.StoreBranchName() || b == c.LocksBranchName() {
				bad("runs.%s is triggered by %s, the store's branch: every write would start it", name, b)
			}
		}
	}
}

func (c *Config) runBranches(name string) []string {
	t := c.Runs[name].Trigger

	var out []string

	if t.Branch != "" {
		out = append(out, t.Branch)
	}

	if t.PullRequests != nil && t.PullRequests.Base != "" {
		out = append(out, t.PullRequests.Base)
	}

	return out
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

var resourceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (c *Config) validateImages(bad func(string, ...any)) {
	for _, name := range c.SuiteNames() {
		if img := c.Suites[name].Image; img != "" && !resourceName.MatchString(img) {
			bad("suites.%s.image must name a resource of the root pipeline file", name)
		}
	}

	if img := c.Concourse.Planner.PrepareImage; img != "" && !resourceName.MatchString(img) {
		bad("concourse.planner.prepare_image must name a resource of the root pipeline file")
	}
}

func (c *Config) validateNarrowing(bad func(string, ...any)) {
	if c.Narrowing < 1 || c.Narrowing > MaxNarrowing || c.NarrowingAlarm < 0 {
		bad("narrowing must be 1 to %d, narrowing_alarm 0 or more", MaxNarrowing)
	}

	for _, name := range slices.Sorted(maps.Keys(c.Narrowings)) {
		n := c.Narrowings[name]

		if n.OffAt < 1 || n.OffAt > MaxNarrowing {
			bad("narrowings.%s: off_at must be 1 to %d", name, MaxNarrowing)
		}

		if strings.TrimSpace(n.Note) == "" {
			bad("narrowings.%s: needs a note, why the narrowing holds up to that level", name)
		}
	}

	for _, p := range c.SampleOn {
		if err := checkGlob(p); err != nil {
			bad("sample_on %q: %v", p, err)
		}
	}
}

func (c *Config) validatePlugins(bad func(string, ...any)) {
	names := map[string]bool{}
	runners := 0

	for _, p := range c.Plugins {
		switch {
		case p.Name == "" || names[p.Name]:
			bad("plugins: every plugin needs a name of its own")
		case p.Kind != KindCorrelator && p.Kind != KindSBOM && p.Kind != KindRunner && p.Kind != KindDriver && p.Kind != KindConnector:
			bad("plugin %s: kind must be correlator, sbom, runner, driver or connector", p.Name)
		case p.Kind == KindConnector && p.Name != ConnectorGitHub && p.Name != ConnectorGit:
			bad("plugin %s: a connector entry pins the %s or %s connector, by that name", p.Name, ConnectorGitHub, ConnectorGit)
		case p.Kind == KindConnector && (p.Release == nil || len(p.Config) > 0):
			bad("plugin %s: a connector entry needs a release (version and one of sha256 or sums), and takes no config: "+
				"the github section and connectors say where the code lives", p.Name)
		case p.Kind == KindDriver && p.Name == BuiltinCommand:
			bad("plugin %s: %s is the driver built into pipelinegen; give the entry another name", p.Name, BuiltinCommand)
		case p.Repository == "":
			bad("plugin %s: repository is missing", p.Name)
		case (p.Release == nil) == (p.Commit == "") || (p.Release != nil && !p.Release.valid()) || (p.Commit != "" && !commitID.MatchString(p.Commit)):
			bad("plugin %s: needs exactly one of release (version and one of sha256 or sums) or commit (40 hex)", p.Name)
		}

		names[p.Name] = true

		switch {
		case p.CollectSeconds < 0:
			bad("plugin %s: collect_seconds must be a positive number of seconds", p.Name)
		case p.CollectSeconds > 0 && p.Kind != KindCorrelator:
			bad("plugin %s: collect_seconds bounds a correlator's collect calls, and this entry's kind is %s", p.Name, p.Kind)
		}

		for _, g := range append(slices.Clone(p.Include), p.Exclude...) {
			if err := checkGlob(g); err != nil {
				bad("plugin %s: pattern %q: %v", p.Name, g, err)
			}
		}

		if p.Kind == KindRunner {
			runners++
		}
	}

	if runners > 1 {
		bad("plugins: at most one runner")
	}
}

func (b BazelDiff) validate(bad func(string, ...any)) {
	if (b.Version == "") != (len(b.SHA256) == 0) {
		bad("bazel_diff needs both a version and a sha256")
	}

	for platform, sum := range b.SHA256 {
		if !sha256Hex.MatchString(sum) || platform != anyPlatform && !platformName.MatchString(platform) {
			bad("bazel_diff.sha256: %q: a checksum is 64 hex digits, by a platform such as linux-amd64 or darwin-arm64", platform)
		}
	}
}
