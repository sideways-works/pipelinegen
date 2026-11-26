// Package config reads pipelinegen.yaml, the one file a repository writes to
// tell pipelinegen about its suites.
package config

import (
	"bytes"
	"cmp"
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
	"github.com/sideways-works/pipelinegen/plugin"
)

const FileName = "pipelinegen.yaml"

const defaultBranch = "main"

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
	Packages Packages `yaml:"packages,omitempty"`
	Images   Images   `yaml:"images,omitempty"`
	Learn    Learn    `yaml:"learn"`
	// Memory says whether a batch waits for the memory its slices' recorded peaks predict, and a
	// planning or bookkeeping step for its own.
	Memory TopMemory `yaml:"memory"`
	// CPU says whether a batch waits for the cores its slices' recorded use predicts and for the
	// worker's CPU pressure to ease, and a planning or bookkeeping step for the pressure.
	CPU      CPU            `yaml:"cpu"`
	Keep     Keep           `yaml:"keep"`
	Writes   Writes         `yaml:"writes"`
	Store    Store          `yaml:"store"`
	Plans    Plans          `yaml:"plans"`
	Runs     map[string]Run `yaml:"runs"`
	Untested []Untested     `yaml:"untested"`
	// Macros declares, by "<.bzl label>%<name>", what another repository's macro reads in the
	// package that calls it: the check can't see inside such a macro.
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
	// RebuildCheck is what a difference between the cached graph and the nightly's cold
	// rebuild does: error (the default) turns the run red, warning only warns.
	RebuildCheck string `yaml:"rebuild_check"`
	// ExplainLimit is how many selected slices a plan asks bazel-diff to explain (0 none);
	// a pointer keeps 0 apart from left out.
	ExplainLimit *int             `yaml:"explain_limit"`
	SampleOn     []string         `yaml:"sample_on"`
	Plugins      []Plugin         `yaml:"plugins"`
	Forges       []Forge          `yaml:"forges"`
	Generated    []map[string]any `yaml:"generated"`

	// groupProblems are what replacing the plugins' groups by their entries met.
	groupProblems []string
}

// Learn is the retired built-in binding's section: nothing reads it, and
// a config that sets it gets a warning.
type Learn struct {
	Modules []string    `yaml:"modules"`
	Pure    []string    `yaml:"pure"`
	Files   []FileUnits `yaml:"files"`
}

type FileUnits struct {
	Paths     []string `yaml:"paths"`
	Normalize string   `yaml:"normalize"`
}

type Plugin struct {
	Name       string `yaml:"name"`
	Kind       string `yaml:"kind"`
	Repository string `yaml:"repository"`
	// Path is a plugin's directory in a repository holding several: its releases are tagged
	// <path>/vX.Y.Z.
	Path           string         `json:"path,omitempty"  yaml:"path,omitempty"`
	Release        *Release       `yaml:"release"`
	Commit         string         `yaml:"commit"`
	Zip            *Zip           `yaml:"zip"`
	Include        []string       `yaml:"include"`
	Exclude        []string       `yaml:"exclude"`
	Config         map[string]any `yaml:"config"`
	Fill           *bool          `yaml:"fill"`
	CollectSeconds int            `yaml:"collect_seconds"`
	// With makes the entry a group: each of its entries takes the group's repository. Loading
	// replaces a group by its entries.
	With []Plugin `json:"-" yaml:"with,omitempty"`
}

// Fills tells whether the local commands fill the plugin's caches.
func (p Plugin) Fills() bool { return p.Fill == nil || *p.Fill }

// DefaultCollectSeconds bounds a correlator's collect call unless its
// entry says otherwise: a collector that hangs must not hold its batch.
const DefaultCollectSeconds = 10

// MaxCollectSeconds bounds a suite's collect calls together: a mark waits for each in turn,
// and the playwright driver's hook (marks.mjs) waits 120 seconds for one at most.
const MaxCollectSeconds = 90

func (p Plugin) CollectLimit() time.Duration {
	if p.CollectSeconds > 0 {
		return time.Duration(p.CollectSeconds) * time.Second
	}

	return DefaultCollectSeconds * time.Second
}

const (
	KindCorrelator = "correlator"
	KindSBOM       = "sbom"
	KindRunner     = "runner"
)

// Runner is the only runner plugin; with several, none is the runner and RunnerNamed picks one.
func (c *Config) Runner() (Plugin, bool) {
	if runners := c.Runners(); len(runners) == 1 {
		return runners[0], true
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
	Files KeepFiles      `yaml:"files"`
}

// KeepFiles caps the failure files a run keeps: each slice's, and the
// whole run's. 0 keeps none.
type KeepFiles struct {
	Slice Size `yaml:"slice"`
	Run   Size `yaml:"run"`
}

const (
	DefaultFilesSlice = Size("2MB")
	DefaultFilesRun   = Size("50MB")
)

// maxFilesSlice is GitHub's largest file: a slice's cap above it would let
// one file fail every push of the run's records.
const maxFilesSlice = 100 << 20

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

// ListsRetention keeps what a Retention keeps and, whatever their age, the lists of the
// default branch's last Commits first-parent commits; none of its commits before those.
type ListsRetention struct {
	Retention `yaml:",inline"`

	Commits int `yaml:"commits"`
}

// DefaultListsCommits is how many of the default branch's last commits keep their lists:
// a pull request not rebased for a while still finds its base's.
const DefaultListsCommits = 200

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

// Writes is how a failed store write goes again: one that failed in passing until its
// deadline, one the remote refused only a few times; one whose ref moved reads again.
type Writes struct {
	// Retries is how often a write that failed in passing goes again at least, however long
	// that takes (0 tries once); a pointer keeps 0 apart from left out.
	Retries *int `yaml:"retries"`
	// Backoff is the first retry's wait; each later one doubles it up to 30 seconds, cut to a
	// random part of it so writers that met don't meet again in step.
	Backoff Period `yaml:"backoff"`
	// Deadline is how long after its first try a write that failed in passing keeps going
	// once Retries are spent; it must be shorter than limits.steps.
	Deadline Period `yaml:"deadline"`
}

const (
	DefaultBackoff       = time.Second
	DefaultWriteDeadline = 10 * time.Minute
)

// Store is where pipelinegen keeps its records in the repository.
type Store struct {
	Backend string `yaml:"backend"`
	// Branch is the records' branch; an older pipelinegen kept its locks' leases on it with
	// "-locks" added, which the plan step sweeps.
	Branch   string `yaml:"branch"`
	Deadline Period `yaml:"deadline"`
	// Plugin names the store plugin that keeps the records, with backend plugin.
	Plugin string `yaml:"plugin"`
	// Credentials name the CI vars of the store plugin's keys: one that writes, for pipelinegen's
	// own steps, and one that only reads, for a step that plans a proposed change.
	Credentials StoreCredentials `yaml:"credentials"`
}

type StoreCredentials struct {
	Write *PlansCredentials `yaml:"write"`
	Read  *PlansCredentials `yaml:"read"`
}

const (
	StoreRefs          = "refs"
	StoreBranch        = "branch"
	StorePlugin        = "plugin"
	DefaultStoreBranch = "pipelinegen"
	defaultDeadline    = 10 * time.Minute
)

func (c *Config) OnBranch() bool { return c.Store.Backend == StoreBranch }

// validateStoreCredentials wants the writing key's vars, and the reading key's for a run of proposed
// changes: its first planning step runs the change's code and may hold no key that writes.
func (c *Config) validateStoreCredentials(bad func(string, ...any)) {
	cr := c.Store.Credentials

	for _, k := range []struct {
		name string
		key  *PlansCredentials
	}{{"write", cr.Write}, {"read", cr.Read}} {
		if k.key == nil {
			continue
		}

		if !varName.MatchString(k.key.AccessKeyID) || !varName.MatchString(k.key.SecretAccessKey) {
			bad("store.credentials.%s needs access_key_id and secret_access_key, each the name of a CI var", k.name)
		}
	}

	if cr.Write == nil {
		bad("store.backend %s needs store.credentials.write, the CI vars of the key pipelinegen's own steps write records with", StorePlugin)
	}

	if cr.Read == nil && slices.ContainsFunc(c.RunNames(), func(n string) bool { return c.Runs[n].Trigger.PullRequests != nil }) {
		bad("store.backend %s with a run of pull requests needs store.credentials.read: the run's first planning step runs the change's code, and may hold only a key that reads", StorePlugin)
	}
}

// KindStore is a store plugin's kind.
const KindStore = plugin.KindStore

// StorePlugin is the plugin entry of the store plugin that keeps the records, with backend plugin.
func (c *Config) StorePlugin() (Plugin, bool) {
	if c.Store.Backend != StorePlugin || c.Store.Plugin == "" {
		return Plugin{}, false
	}

	for _, p := range c.Plugins {
		if p.Kind == KindStore && p.Name == c.Store.Plugin {
			return p, true
		}
	}

	return Plugin{}, false
}

func (c *Config) StoreBranchName() string {
	if c.Store.Branch == "" {
		return DefaultStoreBranch
	}

	return c.Store.Branch
}

// LocksBranchName is the branch an older pipelinegen kept the leases of its
// locks on.
func (c *Config) LocksBranchName() string { return c.StoreBranchName() + "-locks" }

func (c *Config) StoreDeadline() time.Duration {
	if d, ok := c.Store.Deadline.Duration(); ok && c.Store.Deadline != "" {
		return d
	}

	return defaultDeadline
}

const (
	DefaultRetries = 10
	MaxRetries     = 50
)

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

func (c *Config) WriteRetries() int {
	if c.Writes.Retries == nil {
		return DefaultRetries
	}

	return *c.Writes.Retries
}

func (c *Config) WriteBackoff() time.Duration {
	if d, ok := c.Writes.Backoff.Duration(); ok && d > 0 {
		return d
	}

	return DefaultBackoff
}

// WriteDeadline is DefaultWriteDeadline, or half of limits.steps when that is shorter, so a
// step that writes keeps time for its other work.
func (c *Config) WriteDeadline() time.Duration {
	if d, ok := c.Writes.Deadline.Duration(); ok && d > 0 {
		return d
	}

	return min(DefaultWriteDeadline, time.Duration(c.Limits.StepMinutes())*time.Minute/2)
}

// Period is a length of time written with a unit: 30s, 5m, 12h, 5d, 2w.
type Period string

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

// MacroKey splits "@repo//pkg:file.bzl%name" into the .bzl file's label and the macro's name;
// not ok for another form or a .bzl file of this repository, whose macros the check reads.
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

func (c *Config) NarrowingFor(suite string) int {
	if n := c.Suites[suite].Narrowing; n > 0 {
		return n
	}

	return c.Narrowing
}

const (
	BindGraph    = "graph"
	BindRecorded = "recorded"
)

const (
	EmptyAllow  = "allow"
	EmptyRefuse = "refuse"
)

func (c *Config) Records(run, suite string) bool {
	return c.Runs[run].Record && c.Suites[suite].Bind == BindRecorded
}

const (
	FollowExecuted   = "executed"
	FollowDependents = "dependents"
)

const (
	RebuildError   = "error"
	RebuildWarning = "warning"
)

const (
	UncoveredFail       = "fail"
	UncoveredEverything = "everything"
)

type Bazel struct {
	Workspace    string   `yaml:"workspace"`
	Bazelisk     string   `yaml:"bazelisk"`
	StartupFlags []string `yaml:"startup_flags"`
}

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
	// Helpers are patterns of files that hold no test of their own: inputs of the slices that
	// use them, never slices. Go test files are told apart by their test names instead.
	Helpers   []string    `yaml:"helpers"`
	Owner     string      `yaml:"owner"`
	ExtraDeps []ExtraDeps `yaml:"extra_deps"`
	// TextInputs are files some test files read as text: a change to their comments or layout
	// is one those tests can observe, so no narrowing drops it.
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
	// Batches is how many batches a job deals the slices into (default Parallel), all run at
	// once: more, smaller batches lose less unflushed work when a worker goes away.
	Batches int `yaml:"batches"`
	// BatchMinutes deals the selected slices into batches of about this many minutes by their
	// recorded durations, Batches at most.
	BatchMinutes int  `yaml:"batch_minutes"`
	Privileged   bool `yaml:"privileged"`
	// Stack says the batches run long-lived processes whose coverage only a correlator's
	// collect can take: a recording run of the suite refuses to start without one.
	Stack bool `yaml:"stack"`
	// EmptyRecordings is whether a recording the correlators saw nothing in may become an
	// empty list (allow) or not (refuse, the default): a wrong empty answer binds to nothing.
	EmptyRecordings string `yaml:"empty_recordings"`
	Lock            *Lock  `yaml:"lock"`
	Minutes         int    `yaml:"minutes"`
	// Memory is the suite's own over the top level's.
	Memory Memory `yaml:"memory"`
	// CPU is the suite's own over the top level's.
	CPU CPU `yaml:"cpu"`
}

// Memory is how batches share a worker's memory.
type Memory struct {
	// Wait makes a batch wait, before its setup, until the worker has the memory its slices'
	// recorded peaks predict, for as long as the batch could still finish within its time limit.
	// Off, a batch waits at most a few minutes and then leaves its slices not run.
	Wait *bool `yaml:"wait"`
	// Broker is the address of the memory broker of the worker a batch runs on (pipelinegen memory
	// serve), host:port, the host "gateway" for the task's default gateway: a waiting batch asks it,
	// so batches starting at once are granted one after the other.
	Broker string `yaml:"broker"`
}

// TopMemory is the top level's memory: a suite's, and what the pipeline's own steps wait for.
type TopMemory struct {
	Memory `yaml:",inline"`

	// Steps is what a planning or bookkeeping step waits for until its own peaks are recorded.
	Steps StepMemory `yaml:"steps"`
}

// StepMemory is, in MB, what a kind of the pipeline's own steps is expected to use before any of its
// peaks are recorded.
type StepMemory struct {
	Planning    int64 `yaml:"planning"`
	Bookkeeping int64 `yaml:"bookkeeping"`
}

// The kinds of the pipeline's own steps that wait for memory, and what each is expected to use before
// its peaks are recorded.
const (
	StepPlanning    = "planning"
	StepBookkeeping = "bookkeeping"
	// StepWhole is a suite's whole-suite check which, as its prepare (StepPrepare), waits for the
	// pressures alone and keeps no peak.
	StepWhole = "whole"

	defaultPlanningMB    = 4096
	defaultBookkeepingMB = 1024
)

// StepMemoryMB is what a step of kind is expected to use, in MB, before its peaks are recorded.
func (c *Config) StepMemoryMB(kind string) int64 {
	if kind == StepPlanning {
		return cmp.Or(c.Memory.Steps.Planning, defaultPlanningMB)
	}

	return cmp.Or(c.Memory.Steps.Bookkeeping, defaultBookkeepingMB)
}

// validateSharing checks how batches and steps share a worker: memory.steps and cpu.
func (c *Config) validateSharing(bad func(string, ...any)) {
	c.validateStepMemory(bad)
	c.validateCPU(bad)
}

func (c *Config) validateStepMemory(bad func(string, ...any)) {
	if c.Memory.Steps.Planning < 0 || c.Memory.Steps.Bookkeeping < 0 {
		bad("memory.steps: planning and bookkeeping are MB, none of them negative")
	}
}

// MemoryBroker is the address of the memory broker suite's batches ask: the suite's own, else the
// top level's.
func (c *Config) MemoryBroker(suite string) string {
	return cmp.Or(c.Suites[suite].Memory.Broker, c.Memory.Broker)
}

// WaitsForMemory tells whether a batch of suite waits for its predicted memory until it fits.
func (c *Config) WaitsForMemory(suite string) bool {
	if w := c.Suites[suite].Memory.Wait; w != nil {
		return *w
	}

	return c.Memory.Wait != nil && *c.Memory.Wait
}

// CPU is how batches share a worker's cores; the broker that grants them is memory.broker's.
type CPU struct {
	// Wait makes a batch wait, with its wait for memory, until the worker's CPU pressure is below
	// Pressure and, with a broker, until the cores granted lately leave room for its predicted cores.
	Wait *bool `yaml:"wait"`
	// Pressure is the CPU pressure (PSI "some avg10", percent) at or above which a batch waits.
	Pressure *float64 `yaml:"pressure"`
	// Cores is what a batch whose slices have no recorded cores is predicted to use.
	Cores *float64 `yaml:"cores"`
}

// The defaults of cpu: the pressure a batch waits below, in percent, and the cores of a batch with
// no recorded use.
const (
	DefaultCPUPressure = 40
	DefaultCPUCores    = 1
)

// WaitsForCPU tells whether a batch of suite waits for the worker's CPU.
func (c *Config) WaitsForCPU(suite string) bool {
	if w := c.Suites[suite].CPU.Wait; w != nil {
		return *w
	}

	return c.CPU.Wait != nil && *c.CPU.Wait
}

// CPUPressure is the CPU pressure, in percent, at or above which a batch of suite waits: the suite's
// own, else the top level's, else DefaultCPUPressure. The empty suite is the top level's alone.
func (c *Config) CPUPressure(suite string) float64 {
	return *cmp.Or(c.Suites[suite].CPU.Pressure, c.CPU.Pressure, new(float64(DefaultCPUPressure)))
}

// CPUCores is the cores a batch of suite whose slices have no recorded cores is predicted to use.
func (c *Config) CPUCores(suite string) float64 {
	return *cmp.Or(c.Suites[suite].CPU.Cores, c.CPU.Cores, new(float64(DefaultCPUCores)))
}

func (c *Config) validateCPU(bad func(string, ...any)) {
	check := func(where string, cpu CPU) {
		if p := cpu.Pressure; p != nil && (*p <= 0 || *p > 100) {
			bad("%scpu.pressure: a percentage above 0 and at most 100, not %v", where, *p)
		}

		if n := cpu.Cores; n != nil && *n <= 0 {
			bad("%scpu.cores: more than 0 cores, not %v", where, *n)
		}
	}

	check("", c.CPU)

	for _, name := range slices.Sorted(maps.Keys(c.Suites)) {
		check("suites."+name+".", c.Suites[name].CPU)
	}
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

func (s Suite) BatchCount() int {
	if s.Batches > 0 {
		return s.Batches
	}

	return s.ParallelOrDefault()
}

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

	if err := retiredForges(data); err != nil {
		return nil, err
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

// EnvConcourseURL holds Concourse's address when the config leaves concourse.url out:
// a host is the CI's, not the repository's.
const EnvConcourseURL = "PIPELINEGEN_CONCOURSE_URL"

func (c *Config) learnGoingAway() {
	if len(c.Learn.Modules)+len(c.Learn.Pure)+len(c.Learn.Files) > 0 {
		c.Warnings = append(c.Warnings, "learn is no longer read and will be refused: recorded suites bind through sbom and correlator plugins; remove the key")
	}
}

// ErrUnreadable is a commit's config that can't say which suites and test
// files it had.
var ErrUnreadable = errors.New("config can't be read")

// ParseHistory reads an earlier commit's config, which must always load: what is unknown,
// refused or invalid becomes a warning; only one without its suites fails (ErrUnreadable).
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
	c.Plugins, c.groupProblems = ungroup(c.Plugins)

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
	c.validatePlans(bad)
	c.validatePlugins(bad)

	c.validateUntested(bad)
	c.validateMacros(bad)
	c.validateRuns(bad)
	c.validateForges(bad)
	c.validateSSH(bad)
	c.validateSuiteSecrets(bad)

	c.validateTaskSecrets(bad)

	c.validateCorrelators(bad)
	c.validateCollectLimits(bad)
	c.validateLimits(bad)
	c.validateSharing(bad)
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

// Rebuilds tells whether run builds the graph cold to check the cached one: a run that
// selects everything and records, never of pull requests, in a repository bound by plugins.
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

func (c *Config) UntestedFor(file string) (Untested, bool) {
	for _, u := range c.Untested {
		if slices.ContainsFunc(u.Paths, func(p string) bool { return MatchGlob(p, file) }) {
			return u, true
		}
	}

	return Untested{}, false
}

// MatchesEverythingOn tells whether a changed file is one whose change selects every
// slice, and by which pattern.
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

func (s Suite) ExcludesTarget(label string) bool {
	pkg, _, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")

	return slices.ContainsFunc(s.Exclude, func(p string) bool {
		return MatchGlob(p, pkg) || MatchGlob(p, path.Join(pkg, "BUILD.bazel"))
	})
}

// OwnerOf fills the suite's owner template for a file: {dir}, {file}, {base}, and {stem},
// its name cut at the first dot. False without a template: the file's package owns it.
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
		if c.Store.Plugin != "" {
			bad("store.plugin names the store plugin of store.backend %s", StorePlugin)
		}
	case StorePlugin:
		if _, ok := c.StorePlugin(); !ok {
			bad("store.backend %s needs store.plugin, the name of a plugin entry of kind %s", StorePlugin, KindStore)
		}

		c.validateStoreCredentials(bad)
	default:
		bad("store.backend must be %s, %s or %s", StoreRefs, StoreBranch, StorePlugin)
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
	for _, p := range c.groupProblems {
		bad("%s", p)
	}

	names := map[string]bool{}

	for _, p := range c.Plugins {
		switch {
		case p.Name == "" || names[p.Name]:
			bad("plugins: every plugin needs a name of its own")
		case strings.HasPrefix(p.Name, ".") || strings.ContainsAny(p.Name, `/\`):
			// a plugin's directory in a cache is its name, beside the plugin store's own, which start with a dot
			bad("plugin %q: a name may not start with a dot or hold a slash", p.Name)
		case plugin.CheckKind(p.Kind) != nil:
			bad("plugin %s: %v", p.Name, plugin.CheckKind(p.Kind))
		case p.Kind == KindForge && p.Name != ForgeGitHub && p.Name != ForgeGit:
			bad("plugin %s: a forge entry pins the %s or %s forge, by that name", p.Name, ForgeGitHub, ForgeGit)
		case p.Kind == KindForge && (p.Release == nil || len(p.Config) > 0):
			bad("plugin %s: a forge entry needs a release (version and one of sha256 or sums), and takes no config: "+
				"the github section and forges say where the code lives", p.Name)
		case p.Kind == KindDriver && p.Name == BuiltinCommand:
			bad("plugin %s: %s is the driver built into pipelinegen; give the entry another name", p.Name, BuiltinCommand)
		default:
			if problem := p.pinProblem(); problem != "" {
				bad("plugin %s: %s", p.Name, problem)
			}
		}

		names[p.Name] = true

		validateSource(p, bad)

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
