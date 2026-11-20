package config

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
)

// Trusted are the keys deciding where a run reads and writes, which credential serves what, which
// binaries run, which suites each run waits for, and the runner's config: never the change's own.
type Trusted struct {
	GitRemote       string          `json:"git_remote,omitempty"`
	SSHKey          SSHKeys         `json:"ssh_key"`
	KnownHosts      []string        `json:"known_hosts,omitempty"`
	GitHubRepo      string          `json:"github_repository,omitempty"`
	GitHubToken     string          `json:"github_token,omitempty"`
	GitHubTokens    Tokens          `json:"github_tokens"`
	StatusPrefix    string          `json:"github_status_prefix,omitempty"`
	StoreBackend    string          `json:"store_backend,omitempty"`
	StoreBranch     string          `json:"store_branch,omitempty"`
	BazelDiff       BazelDiff       `json:"bazel_diff"`
	Bazelisk        string          `json:"bazel_bazelisk,omitempty"`
	StartupFlags    []string        `json:"bazel_startup_flags,omitempty"`
	Plugins         []TrustedPlugin `json:"plugins,omitempty"`
	Bootstrap       json.RawMessage `json:"concourse_planner_bootstrap,omitempty"`
	Forges          []Forge         `json:"forges,omitempty"`
	ConcourseBranch string          `json:"concourse_branch,omitempty"`
	// RunSuites are the suites each run names; a change can't take a suite out of its own run.
	RunSuites    map[string][]string `json:"run_suites,omitempty"`
	Steps        map[string][]Step   `json:"suite_steps,omitempty"`
	RunnerConfig *json.RawMessage    `json:"runner_config,omitempty"`
	// RunRunners and RunnerConfigs, only with several runners, are each run's owner and each
	// runner's config block by name; with one, RunnerConfig is its block and it owns every run.
	RunRunners    map[string]string          `json:"run_runners,omitempty"`
	RunnerConfigs map[string]json.RawMessage `json:"runner_configs,omitempty"`
}

type TrustedPlugin struct {
	Name       string         `json:"name"`
	Repository string         `json:"repository"`
	Path       string         `json:"path,omitempty"`
	Release    *Release       `json:"release,omitempty"`
	Commit     string         `json:"commit,omitempty"`
	Zip        *Zip           `json:"zip,omitempty"`
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
		RunRunners: c.runRunners(), RunnerConfigs: c.runnerConfigs(),
	}

	if b, err := json.Marshal(c.Concourse.Planner.Bootstrap); err == nil {
		t.Bootstrap = b
	}

	for _, p := range c.Plugins {
		tp := TrustedPlugin{Name: p.Name, Repository: p.Repository, Path: p.Path, Release: p.Release, Commit: p.Commit, Zip: p.Zip}
		if p.Kind == KindDriver {
			tp.Config = p.Config
		}

		t.Plugins = append(t.Plugins, tp)
	}

	t.Steps = c.suiteSteps()

	sort.Slice(t.Plugins, func(i, j int) bool { return t.Plugins[i].Name < t.Plugins[j].Name })

	for _, cn := range c.Forges {
		// a forge's web links move no credential and no write
		cn.Links = ""
		t.Forges = append(t.Forges, cn)
	}

	for _, name := range c.RunNames() {
		if t.RunSuites == nil {
			t.RunSuites = map[string][]string{}
		}

		t.RunSuites[name] = c.Runs[name].RunSuiteNames()
	}

	return t
}

// WithTrusted puts the trusted keys into a proposed change's config and names the keys it edits:
// one spelled otherwise, in another order or with its default written out is unedited.
func (c *Config) WithTrusted(t Trusted) []string {
	own := c.Trusted()
	owners, runners := c.owners(), c.runnerNames()

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
	same("forges", own.Forges, t.Forges)
	same("concourse.branch", own.ConcourseBranch, t.ConcourseBranch)
	same("suites.*.steps", own.Steps, t.Steps)

	c.trustPlain(t)
	c.trustPlugins(t.Plugins)
	c.trustForges(t.Forges)
	c.trustSteps(t.Steps)

	if t.RunnerConfigs != nil {
		c.trustRunnerConfigs(t.RunnerConfigs, same)
	} else {
		c.trustRunnerConfig(t.RunnerConfig, same)
	}

	edited = append(edited, c.trustRunRunners(owners, runners, t)...)

	return append(edited, c.trustRunSuites(t.RunSuites)...)
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

// trustRunSuites gives each run the trusted config's suites besides its own and names the runs
// the change took a suite out of; a suite it adds stays, since it only waits for more.
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

// comparedPlugins are the plugins as WithTrusted compares them: a repository under either
// spelling of its program's is no edit, since both hold the same program.
func comparedPlugins(ps []TrustedPlugin) []TrustedPlugin {
	out := slices.Clone(ps)
	for i := range out {
		out[i].Repository = programRepository(out[i].Repository)
	}

	return out
}

// trustPlugins pins each plugin where the trusted config does; a plugin only the change adds is
// left out, since no step fetched it.
func (c *Config) trustPlugins(trusted []TrustedPlugin) {
	kept := c.Plugins[:0]

	for _, p := range c.Plugins {
		i := slices.IndexFunc(trusted, func(tp TrustedPlugin) bool { return tp.Name == p.Name })
		if i < 0 {
			continue
		}

		p.Repository, p.Path, p.Release, p.Commit, p.Zip = trusted[i].Repository, trusted[i].Path, trusted[i].Release, trusted[i].Commit, trusted[i].Zip
		if p.Kind == KindDriver {
			p.Config = trusted[i].Config
		}

		kept = append(kept, p)
	}

	c.Plugins = kept
}

func (c *Config) trustForges(trusted []Forge) {
	links := map[string]string{}
	for _, cn := range c.Forges {
		links[cn.Name] = cn.Links
	}

	c.Forges = nil

	for _, cn := range trusted {
		cn.Links = links[cn.Name]
		c.Forges = append(c.Forges, cn)
	}
}
