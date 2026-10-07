package config

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
)

// Trusted are the keys that decide where a run reads and writes, which
// credential serves which purpose, which binary a step runs, which suites
// each run waits for, and the runner's config. In a proposed change's run
// they come from the config the pipeline was generated from, never the
// change's own.
type Trusted struct {
	GitRemote       string            `json:"git_remote,omitempty"`
	SSHKey          SSHKeys           `json:"ssh_key"`
	KnownHosts      []string          `json:"known_hosts,omitempty"`
	GitHubRepo      string            `json:"github_repository,omitempty"`
	GitHubToken     string            `json:"github_token,omitempty"`
	GitHubTokens    Tokens            `json:"github_tokens"`
	StatusPrefix    string            `json:"github_status_prefix,omitempty"`
	StoreBackend    string            `json:"store_backend,omitempty"`
	StoreBranch     string            `json:"store_branch,omitempty"`
	BazelDiff       BazelDiff         `json:"bazel_diff"`
	Bazelisk        string            `json:"bazel_bazelisk,omitempty"`
	StartupFlags    []string          `json:"bazel_startup_flags,omitempty"`
	Plugins         []TrustedPlugin   `json:"plugins,omitempty"`
	Bootstrap       json.RawMessage   `json:"concourse_planner_bootstrap,omitempty"`
	LockRepos       map[string]string `json:"lock_repositories,omitempty"`
	Locks           *TrustedLocks     `json:"locks,omitempty"`
	Connectors      []Connector       `json:"connectors,omitempty"`
	ConcourseBranch string            `json:"concourse_branch,omitempty"`
	// RunSuites are the suites each run names; a change can't take a suite
	// out of its own run.
	RunSuites    map[string][]string `json:"run_suites,omitempty"`
	Steps        map[string][]Step   `json:"suite_steps,omitempty"`
	RunnerConfig *json.RawMessage    `json:"runner_config,omitempty"`
}

// TrustedLocks are the locks' other keys, beside their repositories: what a
// lease is decides how a machine is shared with every other run, so a
// proposed change can't raise its own. Nil in trusted keys from an older
// tool: the change's own values stand.
type TrustedLocks struct {
	Suites map[string]TrustedLock `json:"suites"`
	// RunMinutes are, by "<run>/<suite>", the time limits of the locked
	// suites in the runs that set their own: a lease lasts the limit of the
	// suite that takes it.
	RunMinutes map[string]int `json:"run_minutes,omitempty"`
}

// TrustedLock is one suite's lock, without its repository. Minutes is the
// suite's time limit (minutes, or the lock's old minutes), which its
// leases last.
type TrustedLock struct {
	Name    string `json:"name"`
	Minutes int    `json:"minutes"`
	Slots   int    `json:"slots,omitempty"`
	Per     string `json:"per,omitempty"`
	Weight  int    `json:"weight,omitempty"`
}

// TrustedPlugin is where a plugin comes from.
type TrustedPlugin struct {
	Name       string         `json:"name"`
	Repository string         `json:"repository"`
	Release    *Release       `json:"release,omitempty"`
	Commit     string         `json:"commit,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
}

// Trusted is the config's trusted keys, as they are after defaults.
func (c *Config) Trusted() Trusted {
	t := Trusted{
		GitRemote: c.Git.Remote, SSHKey: c.Git.SSHKey, KnownHosts: c.Git.KnownHosts,
		GitHubRepo: c.GitHub.Repository, GitHubToken: c.GitHub.Token, GitHubTokens: c.GitHub.Tokens, StatusPrefix: c.GitHub.StatusPrefix,
		StoreBackend: c.Store.Backend, StoreBranch: c.Store.Branch,
		BazelDiff: c.BazelDiff, Bazelisk: c.Bazel.Bazelisk, StartupFlags: c.Bazel.StartupFlags,
		ConcourseBranch: c.Concourse.Branch, RunnerConfig: c.runnerConfig(),
	}

	if b, err := json.Marshal(c.Concourse.Planner.Bootstrap); err == nil {
		t.Bootstrap = b
	}

	for _, p := range c.Plugins {
		tp := TrustedPlugin{Name: p.Name, Repository: p.Repository, Release: p.Release, Commit: p.Commit}
		if p.Kind == KindDriver {
			tp.Config = p.Config
		}

		t.Plugins = append(t.Plugins, tp)
	}

	t.Steps = c.suiteSteps()

	sort.Slice(t.Plugins, func(i, j int) bool { return t.Plugins[i].Name < t.Plugins[j].Name })

	for _, name := range c.SuiteNames() {
		if l := c.Suites[name].Lock; l != nil && l.Repository != "" {
			if t.LockRepos == nil {
				t.LockRepos = map[string]string{}
			}

			t.LockRepos[name] = l.Repository
		}
	}

	t.Locks = c.trustedLocks()

	for _, cn := range c.Connectors {
		// a connector's web links move no credential and no write
		cn.Links = ""
		t.Connectors = append(t.Connectors, cn)
	}

	for _, name := range c.RunNames() {
		if t.RunSuites == nil {
			t.RunSuites = map[string][]string{}
		}

		t.RunSuites[name] = c.Runs[name].RunSuiteNames()
	}

	return t
}

// WithTrusted puts the trusted keys into a proposed change's config and says
// which keys the change edits: a key spelled otherwise, in another order or
// with its default written out counts as unedited.
func (c *Config) WithTrusted(t Trusted) []string {
	own := c.Trusted()

	var edited []string

	same := func(key string, a, b any) {
		ja, _ := json.Marshal(a) //nolint:errcheck,errchkjson // config values, which marshal
		jb, _ := json.Marshal(b) //nolint:errcheck,errchkjson // config values, which marshal

		if string(ja) != string(jb) {
			edited = append(edited, key)
		}
	}

	same("git.remote", own.GitRemote, t.GitRemote)
	same("git.ssh_key", own.SSHKey, t.SSHKey)
	same("git.known_hosts", own.KnownHosts, t.KnownHosts)
	same("github.repository", own.GitHubRepo, t.GitHubRepo)
	same("github.token", own.GitHubToken, t.GitHubToken)
	same("github.tokens", own.GitHubTokens, t.GitHubTokens)
	same("github.status_prefix", own.StatusPrefix, t.StatusPrefix)
	same("store.backend", own.StoreBackend, t.StoreBackend)
	same("store.branch", own.StoreBranch, t.StoreBranch)
	same("bazel_diff", own.BazelDiff, t.BazelDiff)
	same("bazel.bazelisk", own.Bazelisk, t.Bazelisk)
	same("bazel.startup_flags", own.StartupFlags, t.StartupFlags)
	same("plugins", comparedPlugins(own.Plugins), comparedPlugins(t.Plugins))
	same("concourse.planner.bootstrap", own.Bootstrap, t.Bootstrap)
	same("suites.*.lock.repository", own.LockRepos, t.LockRepos)

	if t.Locks != nil {
		same("suites.*.lock", own.Locks, t.Locks)
	}

	same("connectors", own.Connectors, t.Connectors)
	same("concourse.branch", own.ConcourseBranch, t.ConcourseBranch)
	same("suites.*.steps", own.Steps, t.Steps)

	c.trustPlain(t)
	c.trustPlugins(t.Plugins)
	c.trustConnectors(t.Connectors)
	c.trustSteps(t.Steps)
	c.trustRunnerConfig(t.RunnerConfig, same)

	// a suite given back to a run takes the run's trusted lock minutes too
	edited = append(edited, c.trustRunSuites(t.RunSuites)...)

	c.trustLocks(t.Locks, t.LockRepos)

	return edited
}

func (c *Config) trustPlain(t Trusted) {
	c.Git.Remote, c.Git.SSHKey, c.Git.KnownHosts = t.GitRemote, t.SSHKey, t.KnownHosts
	c.GitHub.Repository, c.GitHub.Token, c.GitHub.Tokens, c.GitHub.StatusPrefix = t.GitHubRepo, t.GitHubToken, t.GitHubTokens, t.StatusPrefix
	c.Store.Backend, c.Store.Branch = t.StoreBackend, t.StoreBranch
	c.BazelDiff, c.Bazel.Bazelisk, c.Bazel.StartupFlags = t.BazelDiff, t.Bazelisk, t.StartupFlags
	c.Concourse.Branch = t.ConcourseBranch

	if len(t.Bootstrap) > 0 {
		var b Bootstrap
		if json.Unmarshal(t.Bootstrap, &b) == nil {
			c.Concourse.Planner.Bootstrap = b
		}
	}
}

// trustRunSuites gives each run the trusted config's suites besides its own
// and names the runs the change took a suite out of; a suite the change adds
// stays, since it only waits for more.
func (c *Config) trustRunSuites(trusted map[string][]string) []string {
	var edited []string

	for _, name := range c.RunNames() {
		r := c.Runs[name]

		var missing []string

		for _, s := range trusted[name] {
			if _, ok := r.Suites[s]; !ok {
				missing = append(missing, s)
			}
		}

		if len(missing) == 0 {
			continue
		}

		suites := maps.Clone(r.Suites)
		if suites == nil {
			suites = map[string]RunSuite{}
		}

		for _, s := range missing {
			suites[s] = RunSuite{}
		}

		r.Suites = suites
		c.Runs[name] = r
		edited = append(edited, "runs."+name+".suites")
	}

	return edited
}

// comparedPlugins are the plugins as WithTrusted compares them: a repository
// under either spelling of its program's is no edit, since both hold the
// same program.
func comparedPlugins(ps []TrustedPlugin) []TrustedPlugin {
	out := slices.Clone(ps)
	for i := range out {
		out[i].Repository = programRepository(out[i].Repository)
	}

	return out
}

// trustPlugins pins each plugin where the trusted config does; a plugin
// only the change adds is left out, since no step fetched it.
func (c *Config) trustPlugins(trusted []TrustedPlugin) {
	kept := c.Plugins[:0]

	for _, p := range c.Plugins {
		i := slices.IndexFunc(trusted, func(tp TrustedPlugin) bool { return tp.Name == p.Name })
		if i < 0 {
			continue
		}

		p.Repository, p.Release, p.Commit = trusted[i].Repository, trusted[i].Release, trusted[i].Commit
		if p.Kind == KindDriver {
			p.Config = trusted[i].Config
		}

		kept = append(kept, p)
	}

	c.Plugins = kept
}

func (c *Config) trustedLocks() *TrustedLocks {
	out := &TrustedLocks{Suites: map[string]TrustedLock{}}

	for _, name := range c.SuiteNames() {
		if l := c.Suites[name].Lock; l != nil {
			out.Suites[name] = TrustedLock{Name: l.Name, Minutes: c.SuiteMinutes("", name), Slots: l.Slots, Per: l.Per, Weight: l.Weight}
		}
	}

	for _, run := range c.RunNames() {
		for suite, rs := range c.Runs[run].Suites {
			if c.Suites[suite].Lock != nil && (rs.Minutes > 0 || rs.LockMinutes > 0) {
				if out.RunMinutes == nil {
					out.RunMinutes = map[string]int{}
				}

				out.RunMinutes[run+"/"+suite] = c.SuiteMinutes(run, suite)
			}
		}
	}

	return out
}

// trustLocks gives each suite the trusted lock, or none, and its trusted
// time limit in every run; nil leaves the change's own.
func (c *Config) trustLocks(t *TrustedLocks, repos map[string]string) {
	defer c.trustLockRepos(repos)

	if t == nil {
		return
	}

	for name, s := range c.Suites {
		s.Lock = nil

		if tl, ok := t.Suites[name]; ok {
			s.Lock = &Lock{Name: tl.Name, Slots: tl.Slots, Per: tl.Per, Weight: tl.Weight}
			s.Minutes = tl.Minutes
		}

		c.Suites[name] = s
	}

	for run, r := range c.Runs {
		for suite, rs := range r.Suites {
			if c.Suites[suite].Lock != nil {
				rs.Minutes, rs.LockMinutes = t.RunMinutes[run+"/"+suite], 0
				r.Suites[suite] = rs
			}
		}
	}
}

func (c *Config) trustLockRepos(repos map[string]string) {
	for name, s := range c.Suites {
		if s.Lock != nil {
			l := *s.Lock
			l.Repository = repos[name]
			s.Lock = &l
			c.Suites[name] = s
		}
	}
}

func (c *Config) trustConnectors(trusted []Connector) {
	links := map[string]string{}
	for _, cn := range c.Connectors {
		links[cn.Name] = cn.Links
	}

	c.Connectors = nil

	for _, cn := range trusted {
		cn.Links = links[cn.Name]
		c.Connectors = append(c.Connectors, cn)
	}
}
