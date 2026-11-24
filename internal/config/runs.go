package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

type GitHub struct {
	Repository string `yaml:"repository"`
	// Token names the Concourse var holding the token, never its value: the token of every
	// purpose Tokens leaves out.
	Token string `yaml:"token"`
	// Tokens split the token by purpose, so a step gets only a token for what it does: a task
	// that runs the change's code, at most one that reads.
	Tokens       Tokens `yaml:"tokens"`
	StatusPrefix string `yaml:"status_prefix"`
}

type Tokens struct {
	Read string `json:"read" yaml:"read"`

	Changes string `json:"changes" yaml:"changes"`

	Report string `json:"report" yaml:"report"`

	Store string `json:"store" yaml:"store"`
}

func (t Tokens) byPurpose() map[string]string {
	return map[string]string{
		plugin.PurposeRead: t.Read, plugin.PurposeChanges: t.Changes,
		plugin.PurposeReport: t.Report, plugin.PurposeStore: t.Store,
	}
}

func (g GitHub) TokenFor(purpose string) string {
	if v := g.Tokens.byPurpose()[purpose]; v != "" {
		return v
	}

	return g.Token
}

// SplitTokens is every purpose's var when the token is split by purpose, nil when one token
// serves them all.
func (g GitHub) SplitTokens() map[string]string {
	if g.Tokens == (Tokens{}) {
		return nil
	}

	out := map[string]string{}
	for _, p := range plugin.Purposes {
		out[p] = g.TokenFor(p)
	}

	return out
}

// Git names the remote of a repository on a host pipelinegen doesn't report to: the records live
// in the remote, and the CI system's job results are the report.
type Git struct {
	// Remote is the repository's URL (https, http, git or file), never with a credential.
	Remote string `yaml:"remote"`
	// SSHKey names, for an ssh remote, the CI var holding each purpose's private key.
	SSHKey SSHKeys `yaml:"ssh_key"`
	// KnownHosts are an ssh remote's host keys, as known_hosts lines: required, since its host
	// is checked strictly.
	KnownHosts []string `yaml:"known_hosts"`
	Origin     string   `yaml:"-"`
}

// Reports tells whether runs report to a host, where statuses, comments and pull requests live;
// without one, only branch, timer and manual runs can run.
func (c *Config) Reports() bool {
	return c.GitHub.Repository != "" || c.GitHub.Token != "" || c.GitHub.Tokens != (Tokens{})
}

// remoteSchemes are those git reaches with nothing but the token pipelinegen's steps carry.
var remoteSchemes = map[string]bool{schemeHTTPS: true, "http": true, "git": true, "file": true}

func validRemote(remote string) bool {
	u, err := url.Parse(remote)

	return err == nil && remoteSchemes[u.Scheme] && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Host != "" || u.Scheme == "file") && u.Path != "" && u.Path != "/"
}

type Concourse struct {
	URL        string         `yaml:"url"`
	Team       string         `yaml:"team"`
	Repository map[string]any `yaml:"repository"`
	// Vars is not read; a config that still sets it loads with a warning, so it isn't taken
	// for doing something.
	Vars    []string `yaml:"vars,omitempty"`
	Planner Planner  `yaml:"planner"`
	Include string   `yaml:"include"`
	Branch  string   `yaml:"branch"`
	// PullRequests is the image of the pull request resource type (default the maintained
	// fork of github-pr-resource).
	PullRequests map[string]any `yaml:"pull_requests"`
	// Mock says whether the Concourse has its bundled mock resource type, which a forge's
	// chain of changes needs: "bundled" (the default) or "missing".
	Mock string `yaml:"mock,omitempty"`
	// SelfSet lets the pipeline job set the pipeline itself; off, it only fails when stale. On,
	// secrets stay ((name)) references, which need a credential manager or var_sources.
	SelfSet      bool                   `yaml:"self_set"`
	SerialGroups map[string]SerialGroup `yaml:"serial_groups,omitempty"`
}

type SerialGroup struct {
	PerRun bool `yaml:"per_run"`
}

// Planner is the image pipelinegen's own steps run in, and the tools they fetch.
type Planner struct {
	Image     map[string]any `yaml:"image"`
	Digest    string         `yaml:"digest"`
	Bootstrap Bootstrap      `yaml:"bootstrap"`
	// Prepare is a shell command run in the checkout before planning to fill the plugins'
	// shared caches (go mod download, say); it may use the network, plugins never do.
	Prepare      string `yaml:"prepare"`
	PrepareImage string `yaml:"prepare_image,omitempty"`
	// Params and Secrets are the prepare command's environment, never pipelinegen's steps';
	// GitToken names the secret git authenticates with there.
	Params   map[string]string `yaml:"params,omitempty"`
	Secrets  map[string]string `yaml:"secrets,omitempty"`
	GitToken string            `yaml:"git_token,omitempty"`
	// Packages are what pipelinegen's own steps need installed in the planner image.
	Packages Packages `yaml:"packages,omitempty"`
}

// Bootstrap is what each job's first step fetches into its tools: the pipelinegen release and
// its checksum, or a commit to build, and bazelisk.
type Bootstrap struct {
	Repository string   `json:"repository" yaml:"repository"`
	Release    *Release `json:"release"    yaml:"release"`
	Commit     string   `json:"commit"     yaml:"commit"`
	Bazelisk   Download `json:"bazelisk"   yaml:"bazelisk"`
}

// Release has exactly one of SHA256, one file's checksum, and Sums, the checksum of the
// release's SHA256SUMS, so one pin serves Linux and macOS alike.
type Release struct {
	Version string `json:"version"        yaml:"version"`
	SHA256  string `json:"sha256"         yaml:"sha256"`
	Sums    string `json:"sums,omitempty" yaml:"sums"`
}

func (r Release) valid() bool {
	if r.Sums != "" {
		return r.Version != "" && r.SHA256 == "" && sha256Hex.MatchString(r.Sums)
	}

	return r.Version != "" && sha256Hex.MatchString(r.SHA256)
}

// Download is a version of a tool that publishes no checksum list, and its binary's checksum.
type Download struct {
	Version string `json:"version" yaml:"version"`
	SHA256  string `json:"sha256"  yaml:"sha256"`
}

var DefaultBazelisk = Download{Version: "v1.30.0", SHA256: "a248ae2b2b62b5baebb748f6fba7203bff1a27e5d01b55399a8406744cb539e8"}

func (d Download) valid() bool {
	return d.Version != "" && sha256Hex.MatchString(d.SHA256)
}

// Pin names the pinned build in keys: the checksum it is checked by, the sums file's when it
// has one.
func (r Release) Pin() string {
	if r.Sums != "" {
		return r.Sums
	}

	return r.SHA256
}

type Run struct {
	Trigger   Trigger             `yaml:"trigger"`
	Compare   string              `yaml:"compare"`
	Select    string              `yaml:"select"`
	Suites    map[string]RunSuite `yaml:"suites"`
	Record    bool                `yaml:"record"`
	Report    RunReport           `yaml:"report"`
	Uncovered string              `yaml:"uncovered"`
	Follow    string              `yaml:"follow"`
	Keep      *Retention          `yaml:"keep"`
	// TurnMinutes is not read and is accepted with a warning; a continuous run's round counts
	// as dead after IdleMinutes.
	TurnMinutes int `yaml:"turn_minutes"`
	IdleMinutes int `yaml:"idle_minutes"`
	// PassedWithin, on a run that selects every slice, leaves out a slice that passed in this run
	// within the period and that nothing changed since reaches: the run then checks the reach
	// analysis only for slices whose pass is older. Off by default.
	PassedWithin Period `yaml:"passed_within"`
	// WaitMinutes is how long a continuous run's newest build waits for the round in progress
	// before it fails naming the round; a newer commit ends the wait at once.
	WaitMinutes int `yaml:"wait_minutes"`
	// Runner names the runner plugin whose pipeline holds the run; others leave it out. Without
	// it, the only runner configured.
	Runner string `yaml:"runner"`
}

// IdleLength is how many minutes a continuous run's round may go without a recorded step before
// it counts as dead: idle_minutes, else the longest silence its limits allow and a margin.
func (c *Config) IdleLength(run string) int {
	r := c.Runs[run]
	if r.IdleMinutes > 0 {
		return r.IdleMinutes
	}

	longest := 0
	for _, suite := range r.RunSuiteNames() {
		longest = max(longest, c.SuiteMinutes(run, suite))
	}

	return c.Limits.StepMinutes() + 2*longest + idleMargin
}

// idleMargin, in minutes, covers the steps around a stretch the limits bound and the workers' clocks.
const idleMargin = 10

// PlanningLength is how many minutes a continuous round with no plan yet may go without a newer
// record before it counts as dead: limits.plan, and a margin for the steps and clocks around it.
func (c *Config) PlanningLength() int {
	return c.Limits.PlanMinutes() + idleMargin
}

// Trigger is what starts a run; exactly one of its fields is set.
type Trigger struct {
	Branch       string        `yaml:"branch"`
	PullRequests *PullRequests `yaml:"pull_requests"`
	// Changes is pull_requests by its host-neutral name, moved into PullRequests when read.
	Changes *PullRequests `yaml:"changes"`
	Timer   *Timer        `yaml:"timer"`
	Manual  bool          `yaml:"manual"`
	// Continuous, beside Branch, queues a build per commit and runs its rounds one at a time: a
	// build whose commit is no longer the tip runs nothing, the tip's waits for the round.
	Continuous bool `yaml:"continuous"`
}

// PullRequests selects the open pull requests a run tests.
type PullRequests struct {
	Base          string   `yaml:"base"`
	Draft         Drafts   `yaml:"draft"`
	Labels        []string `yaml:"labels"`
	LabelsWithout []string `yaml:"labels_without"`
	Head          string   `yaml:"head"`
	// OncePerReady plans a head only when it was the head as the pull request last became ready:
	// a push to a ready pull request starts nothing until it is set to draft and ready again.
	OncePerReady bool `yaml:"once_per_ready"`
	// MissedAfter is how long an open head may go without a plan, or a planned one without a
	// suite begun, before the sweep plans it or hands it on again (default 10m).
	MissedAfter Period `yaml:"missed_after,omitempty"`
	SweepEvery  Period `yaml:"sweep_every,omitempty"`
}

// Drafts is a pull request run's draft setting: "", "false", "true" or "any"; left out it is
// "false".
type Drafts string

const (
	DraftsReady Drafts = "false"
	DraftsOnly  Drafts = "true"
	DraftsAny   Drafts = "any"
)

func (d *Drafts) UnmarshalYAML(n *yaml.Node) error {
	switch v := Drafts(n.Value); {
	case n.Kind == yaml.ScalarNode && (v == DraftsReady || v == DraftsOnly || v == DraftsAny):
		*d = v

		return nil
	default:
		return fmt.Errorf("%w: draft is true, false or any, not %q", ErrInvalid, n.Value)
	}
}

// Wanted is the draft state a pull request must have, nil for either: ready ones only unless
// the config says otherwise, since a draft is work its author hasn't asked anyone to look at.
func (d Drafts) Wanted() *bool {
	switch d {
	case DraftsAny:
		return nil
	case DraftsOnly:
		return new(true)
	default:
		return new(false)
	}
}

const (
	MockBundled = "bundled"
	MockMissing = "missing"
)

// DefaultSweepEvery is how often a forge's changes are swept for.
const DefaultSweepEvery = "1m"

// DefaultMissedAfter is how long a head waits for its own planning build before the sweep
// takes it.
const DefaultMissedAfter = 10 * time.Minute

func (p *PullRequests) Missed() time.Duration {
	if d, ok := p.MissedAfter.Duration(); ok {
		return d
	}

	return DefaultMissedAfter
}

type Timer struct {
	Start    string   `yaml:"start"`
	Stop     string   `yaml:"stop"`
	Location string   `yaml:"location"`
	Days     []string `yaml:"days"`
}

type RunSuite struct {
	Params      map[string]string `yaml:"params"`
	Minutes     int               `yaml:"minutes"`
	LockMinutes int               `yaml:"lock_minutes"`
	Secrets     Secrets           `yaml:"secrets"`
}

type RunReport struct {
	Status    *bool    `yaml:"status"`
	Comment   *Comment `yaml:"comment"`
	Reasoning bool     `yaml:"reasoning"`
	Flush     Period   `yaml:"flush"`
}

// FlushEvery is how often a run's batches push their journals.
func (r RunReport) FlushEvery() time.Duration {
	if d, ok := r.Flush.Duration(); ok {
		return d
	}

	return 30 * time.Second
}

type Comment struct {
	Format string `yaml:"format"`
}

const (
	CompareMergedTarget = "merged_target"
	CompareLastPassed   = "last_passed"
	CompareNone         = "none"
	SelectAffected      = "affected"
	SelectAll           = "all"
)

var (
	commitID  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	runName   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	timeOfDay = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):[0-5][0-9]$`)
)

func (c *Config) UncoveredFor(run string) string {
	if u := c.Runs[run].Uncovered; u != "" {
		return u
	}

	return c.Uncovered
}

func (c *Config) KeepRuns(run string) Retention {
	if k := c.Runs[run].Keep; k != nil {
		return *k
	}

	return c.Keep.Runs
}

// FollowFor is how far run follows a change in suite, and the depth bound (0: none).
func (c *Config) FollowFor(run, suite string) (string, int) {
	follow, depth := c.Follow, c.FollowDepth

	s := c.Suites[suite]
	if s.Follow != "" {
		follow = s.Follow
	}

	if s.FollowDepth > 0 {
		depth = s.FollowDepth
	}

	if r := c.Runs[run].Follow; r != "" {
		follow = r
	}

	return follow, depth
}

func validFollow(f string) bool {
	return f == FollowExecuted || f == FollowDependents
}

func (r RunReport) Statuses() bool {
	return r.Status == nil || *r.Status
}

func (c *Config) RunNames() []string {
	names := make([]string, 0, len(c.Runs))
	for name := range c.Runs {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func (r Run) RunSuiteNames() []string {
	names := make([]string, 0, len(r.Suites))
	for name := range r.Suites {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// RunCovers tells whether a run plans, reports and waits for suite: those its entry names, or
// every suite without a config, the run, or its suites.
func (c *Config) RunCovers(run, suite string) bool {
	if c == nil {
		return true
	}

	r, ok := c.Runs[run]
	if !ok || len(r.Suites) == 0 {
		return true
	}

	_, covered := r.Suites[suite]

	return covered
}

// Params merges a suite's params with the run's for it; the run's win.
func (c *Config) Params(run, suite string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, c.Suites[suite].Params)
	maps.Copy(out, c.Runs[run].Suites[suite].Params)

	return out
}

func (c *Config) validateRuns(bad func(string, ...any)) {
	for _, name := range c.RunNames() {
		c.validateRun(name, c.Runs[name], bad)
	}

	if len(c.Runs) > 0 {
		c.validateCI(bad)
	}
}

// validateCI checks what a pipeline is made from as far as the config has it, so a config still
// being written checks and plans locally; only ValidatePipeline needs it all.
func (c *Config) validateCI(bad func(string, ...any)) {
	if c.Reports() {
		c.validateTokens(bad)
	}

	if c.hasConcourse() {
		c.validateConcourse(bad)
	}
}

func (c *Config) validateTokens(bad func(string, ...any)) {
	g := c.GitHub

	if g.Repository == "" || (g.Token == "" && g.Tokens == (Tokens{})) {
		bad("github needs a repository and the name of its token var")

		return
	}

	for _, p := range plugin.Purposes {
		v := g.Tokens.byPurpose()[p]

		switch {
		case v != "" && !varName.MatchString(v):
			bad("github.tokens.%s must be the name of a var, not %q", p, v)
		case g.TokenFor(p) == "":
			bad("github.tokens.%s is missing: name its var, or github.token for every purpose left out", p)
		}
	}
}

// HasConcourse tells whether the config has a concourse section.
func (c *Config) HasConcourse() bool { return c.hasConcourse() }

func (c *Config) hasConcourse() bool {
	cc := c.Concourse
	bs := cc.Planner.Bootstrap

	return len(cc.Repository) > 0 || len(cc.Planner.Image) > 0 || cc.Planner.Digest != "" ||
		bs.Release != nil || bs.Commit != "" || bs.Bazelisk != (Download{})
}

func (c *Config) validateConcourse(bad func(string, ...any)) {
	if len(c.Concourse.Repository) == 0 || len(c.Concourse.Planner.Image) == 0 {
		bad("concourse needs a repository source and a planner image")
	}

	bs := c.Concourse.Planner.Bootstrap
	if (bs.Release == nil) == (bs.Commit == "") || (bs.Release != nil && !bs.Release.valid()) || (bs.Commit != "" && !commitID.MatchString(bs.Commit)) {
		bad("concourse.planner.bootstrap needs exactly one of release (version and one of sha256 or sums) or commit (40 hex)")
	}

	if bs.Bazelisk != (Download{}) && !bs.Bazelisk.valid() {
		bad("concourse.planner.bootstrap.bazelisk needs a version and a sha256")
	}

	if c.Concourse.Planner.Digest == "" {
		bad("concourse.planner needs the image's digest")
	}
}

func (c *Config) validateNoHost(bad func(string, ...any)) {
	if c.Git.Remote == "" {
		bad("runs need a remote for their records: git.remote, or the github section with a repository and the name of its token var")
	}

	for _, name := range c.RunNames() {
		r := c.Runs[name]

		if r.Trigger.PullRequests != nil && !c.ChangesFromForge() {
			bad("run %s: a changes (pull_requests) trigger needs a host or a forge that lists them: the github section, or forges", name)
		}

		if r.Report.Status != nil && *r.Report.Status {
			bad("run %s: report.status needs a host to post to: the github section", name)
		}
	}
}

// ValidatePipeline checks what making the pipeline needs beyond a valid config, once it
// declares runs.
func (c *Config) ValidatePipeline() error { return c.ValidatePipelineFor(false) }

// ValidatePipelineFor checks the config as ValidatePipeline does, for a runner that takes its CI
// system's settings from its own config when ownSettings: it needs no concourse section then.
func (c *Config) ValidatePipelineFor(ownSettings bool) error {
	if len(c.Runs) == 0 {
		return nil
	}

	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...)))
	}

	if !c.Reports() {
		c.validateNoHost(bad)
	}

	if m := c.Concourse.Mock; m != "" && m != MockBundled && m != MockMissing {
		bad("concourse.mock is %q: %s (the default) or %s", m, MockBundled, MockMissing)
	}

	if !ownSettings && !c.hasConcourse() {
		c.validateConcourse(bad)
	}

	return errors.Join(errs...)
}

func validatePassedWithin(where string, r Run, bad func(string, ...any)) {
	if r.PassedWithin == "" {
		return
	}

	d, ok := r.PassedWithin.Duration()

	switch {
	case !ok || d <= 0:
		bad("%s: passed_within must be a period such as 24h", where)
	case r.Select != SelectAll:
		bad("%s: passed_within is for a run that selects every slice (select: all); a run of what changed leaves out what passed since through the passed-since narrowing", where)
	case r.Trigger.PullRequests != nil:
		bad("%s: passed_within is for a branch's run, not a pull request's", where)
	}
}

func (c *Config) validateRun(name string, r Run, bad func(string, ...any)) {
	where := "run " + name

	if !runName.MatchString(name) {
		bad("%s: name must be lower case letters, digits and dashes", where)
	}

	c.validateTrigger(where, r.Trigger, bad)
	c.validateRunRunner(where, r, bad)

	if r.IdleMinutes != 0 && (!r.Trigger.Continuous || r.IdleMinutes < 1) {
		bad("%s: idle_minutes is for a continuous run, and at least 1", where)
	}

	validatePassedWithin(where, r, bad)

	t := r.Trigger

	switch r.Compare {
	case CompareMergedTarget:
		if t.PullRequests == nil {
			bad("%s: compare merged_target needs a pull_requests trigger", where)
		}
	case CompareLastPassed, CompareNone:
	default:
		bad("%s: compare must be merged_target, last_passed or none", where)
	}

	if r.Uncovered != "" && r.Uncovered != UncoveredFail && r.Uncovered != UncoveredEverything {
		bad("%s: uncovered must be fail or everything", where)
	}

	if _, ok := r.Report.Flush.Duration(); r.Report.Flush != "" && !ok {
		bad("%s: report.flush needs a period such as 30s", where)
	}

	if r.Keep != nil && !r.Keep.valid() {
		bad("%s: keep needs last (a count) or for (12h, 5d, 2w)", where)
	}

	if r.Follow != "" && !validFollow(r.Follow) {
		bad("%s: follow must be executed or dependents", where)
	}

	if r.Select != SelectAffected && r.Select != SelectAll {
		bad("%s: select must be affected or all", where)
	}

	if len(r.Suites) == 0 {
		bad("%s: no suites", where)
	}

	for _, s := range r.RunSuiteNames() {
		if _, ok := c.Suites[s]; !ok {
			bad("%s: no suite %s", where, s)
		}
	}

	if cm := r.Report.Comment; cm != nil {
		if t.PullRequests == nil {
			bad("%s: a comment needs a pull_requests trigger", where)
		}

		if cm.Format != "list" && cm.Format != "mermaid" {
			bad("%s: comment format must be list or mermaid", where)
		}
	}
}

func (c *Config) validateTrigger(where string, t Trigger, bad func(string, ...any)) {
	set := 0

	for _, on := range []bool{t.Branch != "" && t.Timer == nil && !t.Manual, t.PullRequests != nil, t.Timer != nil, t.Manual} {
		if on {
			set++
		}
	}

	if set != 1 {
		bad("%s: trigger needs exactly one of branch, pull_requests, timer or manual", where)
	}

	if (t.Timer != nil || t.Manual) && t.Branch == "" {
		bad("%s: a timer or manual trigger needs the branch it runs on", where)
	}

	if t.Continuous && (t.Branch == "" || t.PullRequests != nil || t.Timer != nil || t.Manual) {
		bad("%s: continuous goes with branch alone (trigger: {branch: main, continuous: true})", where)
	}

	if t.Changes != nil {
		bad("%s: trigger has both changes and pull_requests, which are the same; keep one", where)
	}

	if t.PullRequests != nil && t.PullRequests.Base == "" {
		bad("%s: pull_requests needs a base", where)
	}

	if pr := t.PullRequests; pr != nil && pr.MissedAfter != "" {
		if d, ok := pr.MissedAfter.Duration(); !ok || d <= 0 {
			bad("%s: pull_requests.missed_after needs a period such as 10m", where)
		}
	}

	if pr := t.PullRequests; pr != nil && pr.SweepEvery != "" {
		if d, ok := pr.SweepEvery.Duration(); !ok || d < time.Minute {
			bad("%s: pull_requests.sweep_every needs a period of a minute or more, such as 1m", where)
		}
	}

	if t.Timer != nil && (!timeOfDay.MatchString(t.Timer.Start) || !timeOfDay.MatchString(t.Timer.Stop)) {
		bad("%s: timer needs start and stop as H:MM", where)
	}
}
