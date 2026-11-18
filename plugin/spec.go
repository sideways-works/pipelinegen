package plugin

import "github.com/sideways-works/pipelinegen/internal/secret"

// PipelineRequest asks a runner for the repository's pipeline.
type PipelineRequest struct {
	Request

	Spec Spec   `json:"spec"`
	Root string `json:"root"`
}

// PipelineAnswer is the pipeline, in files in the work directory, and what the runner has to
// say about it short of refusing it.
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
	// OtherRuns name the config's runs other runners own, which this pipeline leaves out; Runs
	// are the runner's own. Runner, with several runners, is this one's entry name, which the
	// pipeline's own call of `pipelinegen pipeline` passes as --for.
	OtherRuns []string    `json:"other_runs,omitempty"`
	Runner    string      `json:"runner,omitempty"`
	Suites    []SpecSuite `json:"suites"`
	Tools     []SpecTool  `json:"tools"`
	// ConfigFiles are the files the pipeline is made from, relative to
	// the repository's root: a change to one sets the pipeline again.
	ConfigFiles []string `json:"config_files"`
	Branch      string   `json:"branch"`
	// Trusted goes byte for byte, as EnvTrusted, to every task that runs pipelinegen and to none that runs
	// the repository's scripts: var names and pins, never a secret, which a runner passes on unread.
	Trusted string `json:"trusted,omitempty"`
	// SSHKeys name, by purpose, the var holding the private ssh key, given in
	// PIPELINEGEN_SSH_KEY_<PURPOSE> under the rules of that purpose's token.
	SSHKeys map[string]string `json:"ssh_keys,omitempty"`
	// Limits are, in minutes, those of the steps that aren't a suite's own tasks; every step a
	// runner generates has one.
	Limits SpecLimits `json:"limits"`
	// PlannerPackages are installed by each of pipelinegen's own steps, never baked into a cached
	// image: those steps hold the writing tokens, and an image's tag moves for whoever can push.
	PlannerPackages *SpecPackages `json:"planner_packages,omitempty"`
	// Images is where cached images are kept and which to build, when the config names a
	// registry.
	Images *SpecImages `json:"images,omitempty"`
}

// CapPackages is a runner's capability to install SpecSuite.Packages and Spec.PlannerPackages
// and to build and use Spec.Images; without it the pipeline installs nothing, and is told so.
const CapPackages = "packages"

type SpecPackages struct {
	// Names are the packages as a person reads them.
	Names []string `json:"names"`
	// Install is a POSIX shell script run as root in a subshell before a task's command: it installs what the
	// image lacks (nothing when it has all), downloads into EnvPackageCache, and fails if one can't be had.
	Install string `json:"install"`
	// Images maps each registry-image resource the steps run in that has a cached image to its
	// key in Images.Builds; the steps run in that image, where Install finds everything.
	Images map[string]string `json:"images,omitempty"`
}

// SpecImages are the cached images: a base image with a suite's packages
// installed, built once and pushed to Registry tagged with its key.
type SpecImages struct {
	Registry string `json:"registry"`
	// Credentials is the var of the login that pushes, given only to the job that builds the
	// images; PullCredentials replaces it for pulls. A step running a change's code gets neither.
	Credentials     string           `json:"credentials,omitempty"`
	PullCredentials string           `json:"pull_credentials,omitempty"`
	Builds          []SpecImageBuild `json:"builds,omitempty"`
}

// SpecImageBuild is one cached image: Install run as root on Base at Digest, pushed as
// Registry:Key unless that tag exists; the key is the tool's, a runner never works it out.
type SpecImageBuild struct {
	Key     string   `json:"key"`
	Base    string   `json:"base"`
	Digest  string   `json:"digest"`
	Names   []string `json:"names"`
	Install string   `json:"install"`
}

// EnvPackageCache is where, of the task's cache, the package manager keeps its downloads;
// EnvPackageClean, set, removes its indexes and downloads afterwards, for an image that is kept.
const (
	EnvPackageCache = "PIPELINEGEN_PACKAGE_CACHE"
	EnvPackageClean = "PIPELINEGEN_PACKAGE_CLEAN"
)

// SpecLimits are in minutes.
type SpecLimits struct {
	Plan  int `json:"plan"`
	Steps int `json:"steps"`
}

// EnvLimit ("<key>=<minutes>" from the command's start) lets a step end just before the CI kills
// it and name the key; EnvDeadline, set by a task's script at its start, is the end in Unix seconds.
const (
	EnvLimit    = "PIPELINEGEN_LIMIT"
	EnvDeadline = "PIPELINEGEN_DEADLINE"
)

// LimitMarginSeconds is how long before its limit's end a step of
// pipelinegen's ends on its own, so it can say why and free what it holds.
const LimitMarginSeconds = 120

// TriggerPullRequests is the kind of a run of a repository's open pull requests (or a
// connector's changes), each new head planned in two steps.
const TriggerPullRequests = "pull_requests"

// TriggerContinuous plans every commit of the branch, side by side: a commit no longer the tip
// ends at once, and the tip records its round and waits while an older round is in progress.
const TriggerContinuous = "continuous"

// CapContinuous is a runner's capability to build a continuous run's
// jobs: the planning job taking every commit of the branch.
const CapContinuous = "continuous"

// CapSteps is a runner's capability to write a driver suite's tasks (SpecSuite.Steps): a batch
// task running `pipelinegen slice run`, a prepare task running `pipelinegen slice prepare`.
const CapSteps = "steps"

// CapSSHKeys is a runner's capability to hand steps the ssh keys of Spec.SSHKeys.
const CapSSHKeys = "ssh_keys"

// CapConnectorChanges is a runner's capability to build the chain of a run whose changes come from a
// connector.
const CapConnectorChanges = "connector_changes"

// CapPlacement is a runner's capability to place steps on workers by a placement block of its
// own config; the spec carries no worker tags, so a runner without it is refused.
const CapPlacement = "placement"

const EnvTrusted = "PIPELINEGEN_TRUSTED"

// EnvPipelineStep, set to 1, marks a step that runs the tool: Bazel's output in the step's
// cache, a Bazel server that stops when idle, and git without the worker's own config.
const EnvPipelineStep = secret.PipelineStepVar

type SpecGitHub struct {
	Repository string `json:"repository"`
	// TokenVar is the var of the one token every purpose takes; with tokens split, the store's,
	// for a runner that knows no purposes.
	TokenVar     string `json:"token_var"`
	StatusPrefix string `json:"status_prefix"`
	// Tokens name each purpose's var when the config splits them; a runner hands each step only
	// the purposes it needs, a task that runs the change's code never more than PurposeRead.
	Tokens map[string]string `json:"tokens,omitempty"`
}

const (
	// PurposeRead fetches the code: the repository's commits, and the
	// private modules a task downloads.
	PurposeRead = "read"
	// PurposeChanges lists pull requests and reads their state.
	PurposeChanges = "changes"
	// PurposeReport sets commit statuses and pull request comments.
	PurposeReport = "report"
	// PurposeStore writes the records and the plugins' answers.
	PurposeStore = "store"
)

var Purposes = []string{PurposeRead, PurposeChanges, PurposeReport, PurposeStore}

type SpecRun struct {
	Name        string         `json:"name"`
	Trigger     SpecTrigger    `json:"trigger"`
	Suites      []SpecRunSuite `json:"suites"`
	WaitMinutes int            `json:"wait_minutes,omitempty"`
}

// SpecTrigger is what starts a run: Kind is branch, pull_requests, timer or manual.
type SpecTrigger struct {
	Kind          string     `json:"kind"`
	Branch        string     `json:"branch,omitempty"`
	Base          string     `json:"base,omitempty"`
	Draft         *bool      `json:"draft,omitempty"`
	Labels        []string   `json:"labels,omitempty"`
	LabelsWithout []string   `json:"labels_without,omitempty"`
	Head          string     `json:"head,omitempty"`
	Timer         *SpecTimer `json:"timer,omitempty"`
	// Connector names the connector a run's changes come from, when not from the CI system's own
	// pull request support; the runner builds the connector's chain for it.
	Connector string `json:"connector,omitempty"`
	// SweepEvery is how often the run's sweep looks for changes and for heads its planning
	// missed (such as 1m); empty, the runner's own default.
	SweepEvery string `json:"sweep_every,omitempty"`
}

// SpecTimer is a daily window.
type SpecTimer struct {
	Start    string   `json:"start"`
	Stop     string   `json:"stop"`
	Location string   `json:"location,omitempty"`
	Days     []string `json:"days,omitempty"`
}

type SpecRunSuite struct {
	Suite   string            `json:"suite"`
	Params  map[string]string `json:"params,omitempty"`
	Records bool              `json:"records"`
	// Minutes is the suite's time limit in this run, of each of its tasks
	// on its own: the prepare, each batch, each whole-suite check.
	Minutes    int    `json:"minutes"`
	MinutesKey string `json:"minutes_key"`
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

type SpecSuite struct {
	Name        string `json:"name"`
	Prepare     string `json:"prepare,omitempty"`
	RunSlice    string `json:"run_slice"`
	Image       string `json:"image,omitempty"`
	SerialGroup string `json:"serial_group,omitempty"`
	MaxInFlight int    `json:"max_in_flight,omitempty"`
	// Parallel is how many batches a build of the suite is dealt at least; a runner runs every
	// batch `pipelinegen batches` deals at once.
	Parallel   int  `json:"parallel"`
	Privileged bool `json:"privileged,omitempty"`
	// Lock is never written: only an older tool's spec holds one, which a
	// runner takes no lock for and says so in a notice.
	Lock       *SpecLock        `json:"lock,omitempty"`
	WholeSuite []SpecWholeSuite `json:"whole_suite,omitempty"`
	// Steps, for a suite run by drivers, replaces Prepare and RunSlice:
	// the runner writes the suite's tasks itself (CapSteps).
	Steps *SpecSteps `json:"steps,omitempty"`
	// Packages are what the suite's steps need installed (CapPackages), in the tasks a runner
	// writes, or in the cached images where Packages.Images names one.
	Packages *SpecPackages `json:"packages,omitempty"`
}

// SpecSteps is a suite run by drivers: its batch task runs `pipelinegen slice run` in the suite's
// image with a job cache; when Prepare, `slice prepare` runs in PrepareImage or the suite's.
type SpecSteps struct {
	Prepare      bool     `json:"prepare,omitempty"`
	PrepareImage string   `json:"prepare_image,omitempty"`
	Drivers      []string `json:"drivers,omitempty"`
}

// SpecLock is the lock an older tool's spec gave a suite. No step takes it.
type SpecLock struct {
	Name    string `json:"name"`
	Minutes int    `json:"minutes"`
	Slots   int    `json:"slots,omitempty"`
	Per     string `json:"per,omitempty"`
}

type SpecWholeSuite struct {
	Name       string `json:"name"`
	Task       string `json:"task"`
	Privileged bool   `json:"privileged,omitempty"`
}

// SpecTool is one pinned executable a job fetches: a release and its checksum, or a commit to
// build.
type SpecTool struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	Version    string `json:"version,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// Sums, in place of SHA256, checks the release's SHA256SUMS, kept beside the tool as
	// <name>.SHA256SUMS; the platform's file (<program>-<os>-<arch> or <program>.mjs) is checked by it.
	Sums   string `json:"sums,omitempty"`
	Commit string `json:"commit,omitempty"`
}

const (
	ToolPipelinegen = "pipelinegen"
	ToolBazelisk    = "bazelisk"
)
