package plugin

import "github.com/sideways-works/pipelinegen/internal/secret"

// PipelineRequest asks a runner for the repository's pipeline.
type PipelineRequest struct {
	Request

	Spec Spec   `json:"spec"`
	Root string `json:"root"`
}

// PipelineAnswer is the pipeline, in files in the work directory, and
// what the runner has to say about it short of refusing it.
type PipelineAnswer struct {
	Answer

	// Notices are what the runner wants a person to know of the pipeline it
	// wrote, one line each; the check reports them without failing.
	Notices []string `json:"notices,omitempty"`
}

// Spec is what a runner needs to know of a repository's config.
type Spec struct {
	Pipeline string         `json:"pipeline"`
	GitHub   SpecGitHub     `json:"github"`
	CI       map[string]any `json:"ci"`
	Runs     []SpecRun      `json:"runs"`
	Suites   []SpecSuite    `json:"suites"`
	Tools    []SpecTool     `json:"tools"`
	// ConfigFiles are the files the pipeline is made from, relative to
	// the repository's root: a change to one sets the pipeline again.
	ConfigFiles []string `json:"config_files"`
	Branch      string   `json:"branch"`
	// Trusted is what a runner gives, byte for byte, as EnvTrusted to every
	// task that runs pipelinegen itself and to no task that runs the
	// repository's own scripts. It holds var names and pins, never a secret,
	// so it may stand in the pipeline as a literal; a runner passes it on
	// and never reads it.
	Trusted string `json:"trusted,omitempty"`
	// SSHKeys name, by purpose (read, store), the CI system's var holding
	// the private ssh key. A runner gives a step a purpose's key under the
	// same rules as that purpose's token, in PIPELINEGEN_SSH_KEY_<PURPOSE>:
	// the read key alone to a step that runs a change's code, neither to a
	// task of the repository's.
	SSHKeys map[string]string `json:"ssh_keys,omitempty"`
	// Limits are the time limits, in minutes, of the steps that aren't a
	// suite's own tasks; every step a runner generates has one.
	Limits SpecLimits `json:"limits"`
}

// SpecLimits are the time limits of the pipeline's own steps, in minutes.
type SpecLimits struct {
	Plan     int `json:"plan"`
	Steps    int `json:"steps"`
	LockWait int `json:"lock_wait"`
}

// A step's time limit reaches pipelinegen's own steps so that a step ends a
// little before the CI system kills it and says which key raises it:
// EnvLimit holds "<key>=<minutes>", counted from the command's start;
// EnvDeadline, when a task's script set it at its own start, is the limit's
// end in Unix seconds, and wins.
const (
	EnvLimit    = "PIPELINEGEN_LIMIT"
	EnvDeadline = "PIPELINEGEN_DEADLINE"
)

// LimitMarginSeconds is how long before its limit's end a step of
// pipelinegen's ends on its own, so it can say why and free what it holds.
const LimitMarginSeconds = 120

// LockWaitMinutes is how long `lock acquire` waits for a slot unless its
// --wait says otherwise.
const LockWaitMinutes = 240

// TriggerPullRequests is the kind of a run of a repository's open pull
// requests (or a connector's changes), each new head planned in two steps.
const TriggerPullRequests = "pull_requests"

// TriggerContinuous is the kind of a run whose every commit of the branch
// queues a build of the planning job: the plan step of a commit that is
// no longer the branch's tip ends at once, and the tip's waits while a
// round of the run is in progress.
const TriggerContinuous = "continuous"

// CapContinuous is a runner's capability to build a continuous run's
// jobs: the planning job taking every commit of the branch.
const CapContinuous = "continuous"

// CapSerialJobs is a runner's capability to run a job's builds one at a
// time, in the order they were queued, a queued one waiting without
// running anything: SpecRun.SerialPlan asks it of a planning job.
const CapSerialJobs = "serial_jobs"

// CapLockPerBatch is a runner's capability to take a suite's lock around
// each batch and each whole-suite check, freed in an ensure, for a lock
// with per: batch.
const CapLockPerBatch = "lock_per_batch"

// CapSteps is a runner's capability to write the tasks of a suite run by
// drivers (SpecSuite.Steps): a batch task that runs `pipelinegen slice
// run` and a prepare task that runs `pipelinegen slice prepare`.
const CapSteps = "steps"

// CapSSHKeys is a runner's capability to hand steps the ssh keys of
// Spec.SSHKeys.
const CapSSHKeys = "ssh_keys"

// CapConnectorChanges is a runner's capability to build the chain of a
// run whose changes come from a connector.
const CapConnectorChanges = "connector_changes"

// EnvSerialPlan is the environment variable, set to 1, a runner gives the
// plan step of a planning job it runs one build at a time (SpecRun's
// SerialPlan): without it, a continuous run's plan step records its
// round's start and planning, since two builds may decide at once.
const EnvSerialPlan = "PIPELINEGEN_SERIAL_PLAN"

// CapPlacement is a runner's capability to place the pipeline's steps on
// workers by a placement block of its own config, in its own words. The
// spec carries no worker tags: a runner without it would read them from
// keys no config sets any more, and is refused.
const CapPlacement = "placement"

// EnvTrusted is the environment variable a runner gives Spec.Trusted in.
const EnvTrusted = "PIPELINEGEN_TRUSTED"

// EnvPipelineStep is the environment variable, set to 1, a runner gives
// every step that runs the tool: it runs as a pipeline's step, with Bazel's
// output and downloads in the step's cache, a Bazel server that stops when
// idle, and git without the worker's own config.
const EnvPipelineStep = secret.PipelineStepVar

// SpecGitHub is where statuses go.
type SpecGitHub struct {
	Repository string `json:"repository"`
	// TokenVar is the var of the one token every purpose takes; with
	// tokens split by purpose, the store's, for a runner that knows no
	// purposes.
	TokenVar     string `json:"token_var"`
	StatusPrefix string `json:"status_prefix"`
	// Tokens name the var of each purpose's token when the config splits
	// them; absent when one token serves them all. A runner hands each step
	// only the purposes it needs: a task that runs the change's code never
	// more than PurposeRead.
	Tokens map[string]string `json:"tokens,omitempty"`
}

// The purposes a token serves.
const (
	// PurposeRead fetches the code: the repository's commits, and the
	// private modules a task downloads.
	PurposeRead = "read"
	// PurposeChanges lists pull requests and reads their state.
	PurposeChanges = "changes"
	// PurposeReport sets commit statuses and pull request comments.
	PurposeReport = "report"
	// PurposeStore writes the records, the plugins' answers and the
	// locks.
	PurposeStore = "store"
)

// Purposes lists the purposes in a stable order.
var Purposes = []string{PurposeRead, PurposeChanges, PurposeReport, PurposeStore}

// SpecRun is one run.
type SpecRun struct {
	Name        string         `json:"name"`
	Trigger     SpecTrigger    `json:"trigger"`
	Suites      []SpecRunSuite `json:"suites"`
	WaitMinutes int            `json:"wait_minutes,omitempty"`
	// SerialPlan asks the runner to run the planning job's builds one at a
	// time, in the order queued (CapSerialJobs), and set EnvSerialPlan on
	// the plan step. Nothing else of a pipeline is serialized unless a suite
	// asks for it.
	SerialPlan bool `json:"serial_plan,omitempty"`
}

// SpecTrigger is what starts a run: Kind is branch, pull_requests, timer
// or manual.
type SpecTrigger struct {
	Kind          string     `json:"kind"`
	Branch        string     `json:"branch,omitempty"`
	Base          string     `json:"base,omitempty"`
	Draft         *bool      `json:"draft,omitempty"`
	Labels        []string   `json:"labels,omitempty"`
	LabelsWithout []string   `json:"labels_without,omitempty"`
	Head          string     `json:"head,omitempty"`
	Timer         *SpecTimer `json:"timer,omitempty"`
	// Connector names the connector a run's changes come from, when not from
	// the CI system's own pull request support; the runner builds the
	// connector's chain for it.
	Connector string `json:"connector,omitempty"`
	// SweepEvery is how often the run's sweep looks for changes and for
	// heads its planning missed (a period such as 1m), when the config
	// sets it or the changes come from a connector; empty, the runner's
	// own default.
	SweepEvery string `json:"sweep_every,omitempty"`
}

// SpecTimer is a daily window.
type SpecTimer struct {
	Start    string   `json:"start"`
	Stop     string   `json:"stop"`
	Location string   `json:"location,omitempty"`
	Days     []string `json:"days,omitempty"`
}

// SpecRunSuite is a suite's part in a run.
type SpecRunSuite struct {
	Suite   string            `json:"suite"`
	Params  map[string]string `json:"params,omitempty"`
	Records bool              `json:"records"`
	// Minutes is the suite's time limit in this run: of each of its tasks
	// on its own (the prepare, each batch, each whole-suite check), or,
	// when its lock is taken per build, of all its work while it holds
	// the lock.
	Minutes    int    `json:"minutes"`
	MinutesKey string `json:"minutes_key"`
	// LockMinutes is Minutes for a suite with a lock (0 without), for
	// runners that read only it.
	LockMinutes int `json:"lock_minutes,omitempty"`
	// Secrets are the suite's merged with the run's, by kind of task: a
	// runner gives each task its kind's and no other.
	Secrets SpecSecrets `json:"secrets,omitzero"`
}

// SpecSecrets map, per kind of task, an environment variable's name to the
// name of the CI system's var that fills it.
type SpecSecrets struct {
	Prepare map[string]string `json:"prepare,omitempty"`
	Batch   map[string]string `json:"batch,omitempty"`
	Whole   map[string]string `json:"whole,omitempty"`
}

// SpecSuite is one suite.
type SpecSuite struct {
	Name        string           `json:"name"`
	Prepare     string           `json:"prepare,omitempty"`
	RunSlice    string           `json:"run_slice"`
	Image       string           `json:"image,omitempty"`
	SerialGroup string           `json:"serial_group,omitempty"`
	MaxInFlight int              `json:"max_in_flight,omitempty"`
	Parallel    int              `json:"parallel"`
	Privileged  bool             `json:"privileged,omitempty"`
	Lock        *SpecLock        `json:"lock,omitempty"`
	WholeSuite  []SpecWholeSuite `json:"whole_suite,omitempty"`
	// Steps, for a suite run by drivers, replaces Prepare and RunSlice:
	// the runner writes the suite's tasks itself (CapSteps).
	Steps *SpecSteps `json:"steps,omitempty"`
}

// SpecSteps is what a runner needs of a suite run by drivers. Its batch
// task runs `pipelinegen slice run` in the suite's image, and keeps a
// cache with its job; its prepare task, when Prepare, runs `pipelinegen
// slice prepare` in PrepareImage, else in the suite's image.
type SpecSteps struct {
	Prepare      bool     `json:"prepare,omitempty"`
	PrepareImage string   `json:"prepare_image,omitempty"`
	Drivers      []string `json:"drivers,omitempty"`
}

// SpecLock is a lease between pipelines.
type SpecLock struct {
	Name    string `json:"name"`
	Minutes int    `json:"minutes"`
	Slots   int    `json:"slots,omitempty"`
	// Per is "batch" when each batch takes the lock around itself (and
	// each whole-suite check around itself), in every run; empty, the
	// suite's build takes it around all its work, except in a continuous
	// run, which always takes it per batch.
	Per string `json:"per,omitempty"`
}

// SpecWholeSuite is a check of a suite as a whole.
type SpecWholeSuite struct {
	Name       string `json:"name"`
	Task       string `json:"task"`
	Privileged bool   `json:"privileged,omitempty"`
}

// SpecTool is one pinned executable a job fetches: a release and its
// checksum, or a commit to build.
type SpecTool struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	Version    string `json:"version,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// Sums, in place of SHA256, is the checksum of the release's
	// SHA256SUMS: the bootstrap fetches that file, checks it by Sums, keeps
	// it beside the tool as <name>.SHA256SUMS, and checks the file for its
	// platform (<program>-<os>-<arch>, or <program>.mjs, the program being
	// the one the repository holds: Program) by its line there.
	Sums   string `json:"sums,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// The kinds of SpecTool besides the plugin kinds.
const (
	ToolPipelinegen = "pipelinegen"
	ToolBazelisk    = "bazelisk"
)
