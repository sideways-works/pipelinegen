// Package spec turns a config into the spec a runner plugin gets: defaults applied and per-run
// values worked out, so a runner needs no copy of the config's rules.
package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/internal/packages"
	"github.com/sideways-works/pipelinegen/plugin"
)

var ErrSpec = errors.New("spec")

// From makes the spec of a config whose root pipeline file isn't at hand: it names no cached
// image.
func From(cfg *config.Config) (plugin.Spec, error) { return FromInclude(cfg, nil) }

// FromInclude makes the spec of a config; include is the root pipeline file's text, whose
// registry-image resources are the cached images' bases.
func FromInclude(cfg *config.Config, include []byte) (plugin.Spec, error) {
	return ForRunner(cfg, include, false)
}

// ForRunner makes the spec of a config for its runner; ownSettings, the runner's CapOwnSettings,
// spares a config without a concourse section the Concourse checks, and leaves the spec's CI empty.
func ForRunner(cfg *config.Config, include []byte, ownSettings bool) (plugin.Spec, error) {
	concourse := !ownSettings || cfg.HasConcourse()

	// only a pipeline links to Concourse: no other command needs its address, so a missing one
	// fails here and nowhere else
	if concourse && len(cfg.Runs) > 0 && cfg.Concourse.URL == "" {
		return plugin.Spec{}, fmt.Errorf("%w: concourse needs its url: concourse.url, or %s in the environment", ErrSpec, config.EnvConcourseURL)
	}

	// a config may declare runs before it names their remote and CI system; only a pipeline needs
	// them
	if err := cfg.ValidatePipelineFor(ownSettings); err != nil {
		return plugin.Spec{}, fmt.Errorf("%w: %w", ErrSpec, err)
	}

	// a runner with settings of its own gets no concourse section
	var ci map[string]any

	if concourse {
		m, err := asMap(cfg.Concourse)
		if err != nil {
			return plugin.Spec{}, err
		}

		ci = m
	}

	s := plugin.Spec{
		Pipeline: cfg.Pipeline,
		GitHub: plugin.SpecGitHub{
			Repository: cfg.GitHub.Repository, TokenVar: cfg.GitHub.TokenFor(plugin.PurposeStore), StatusPrefix: cfg.GitHub.StatusPrefix,
			Tokens: cfg.GitHub.SplitTokens(),
		},
		CI:          ci,
		Branch:      cfg.Concourse.Branch,
		ConfigFiles: []string{config.FileName},
		Tools:       tools(cfg),
		Limits:      plugin.SpecLimits{Plan: cfg.Limits.PlanMinutes(), Steps: cfg.Limits.StepMinutes()},
	}

	trusted, err := json.Marshal(cfg.Trusted())
	if err != nil {
		return plugin.Spec{}, fmt.Errorf("%w: the trusted keys: %w", ErrSpec, err)
	}

	s.Trusted = string(trusted)

	if cfg.UsesSSH() {
		s.SSHKeys = map[string]string{}

		for _, p := range gitssh.Purposes {
			if v := cfg.Git.SSHKey.For(p); v != "" {
				s.SSHKeys[p] = v
			}
		}
	}

	if p, ok := cfg.StorePlugin(); ok && cfg.Store.Credentials.Write != nil {
		w := cfg.Store.Credentials.Write
		s.Store = &plugin.SpecStore{Plugin: p.Name, Write: plugin.SpecKey{AccessKeyID: w.AccessKeyID, SecretAccessKey: w.SecretAccessKey}}

		if r := cfg.Store.Credentials.Read; r != nil {
			s.Store.Read = &plugin.SpecKey{AccessKeyID: r.AccessKeyID, SecretAccessKey: r.SecretAccessKey}
		}
	}

	if p := cfg.Plans.S3; p != nil {
		s.Plans = &plugin.SpecPlans{AccessKeyID: p.Credentials.AccessKeyID, SecretAccessKey: p.Credentials.SecretAccessKey}
	}

	if cfg.Concourse.Include != "" {
		s.ConfigFiles = append(s.ConfigFiles, cfg.Concourse.Include)
	}

	for _, name := range cfg.SuiteNames() {
		s.Suites = append(s.Suites, suite(cfg, name))
	}

	withPackages(&s, cfg, include)

	for _, name := range cfg.RunNames() {
		s.Runs = append(s.Runs, run(cfg, name))
	}

	return s, nil
}

func suite(cfg *config.Config, name string) plugin.SpecSuite {
	su := cfg.Suites[name]

	parallel := su.Parallel
	if parallel == 0 {
		parallel = config.DefaultParallel
	}

	out := plugin.SpecSuite{
		Name: name, Prepare: su.Prepare, RunSlice: su.RunSlice, Image: su.Image,
		Parallel: parallel, Privileged: su.Privileged,
	}

	if su.HasSteps() {
		st := &plugin.SpecSteps{}

		for _, i := range su.StepsOf(config.StepPrepare) {
			st.Prepare = true
			if img := su.Steps[i].Image; img != "" {
				st.PrepareImage = img
			}
		}

		for _, step := range su.Steps {
			if d := step.Driver(); d != config.BuiltinCommand && !slices.Contains(st.Drivers, d) {
				st.Drivers = append(st.Drivers, d)
			}
		}

		out.Steps = st
	}

	for _, w := range su.WholeSuite {
		out.WholeSuite = append(out.WholeSuite, plugin.SpecWholeSuite{Name: w.Name, Task: w.Task, Privileged: w.Privileged || su.Privileged})
	}

	return out
}

func run(cfg *config.Config, name string) plugin.SpecRun {
	r := cfg.Runs[name]
	t := r.Trigger

	tr := plugin.SpecTrigger{Branch: t.Branch}

	switch {
	case t.PullRequests != nil:
		pr := t.PullRequests
		tr.Kind, tr.Base, tr.Draft, tr.Labels, tr.LabelsWithout, tr.Head = plugin.TriggerPullRequests, pr.Base, pr.Draft.Wanted(), pr.Labels, pr.LabelsWithout, pr.Head
		tr.SweepEvery = string(pr.SweepEvery)

		if cc := cfg.CodeForge(); cc != nil && cfg.ChangesFromForge() {
			tr.Forge = cc.Name

			if tr.SweepEvery == "" {
				tr.SweepEvery = config.DefaultSweepEvery
			}
		}
	case t.Timer != nil:
		tr.Kind = "timer"
		tr.Timer = &plugin.SpecTimer{Start: t.Timer.Start, Stop: t.Timer.Stop, Location: t.Timer.Location, Days: t.Timer.Days}
	case t.Manual:
		tr.Kind = "manual"
	case t.Continuous:
		tr.Kind = plugin.TriggerContinuous
	default:
		tr.Kind = "branch"
	}

	out := plugin.SpecRun{Name: name, Trigger: tr}
	if t.Continuous {
		out.WaitMinutes = r.WaitLength()
	}

	for _, s := range r.RunSuiteNames() {
		sec := cfg.SecretsFor(name, s)
		rs := plugin.SpecRunSuite{
			Suite: s, Params: cfg.Params(name, s), Records: cfg.Records(name, s),
			Secrets: plugin.SpecSecrets{Prepare: sec.Prepare, Batch: sec.Batch, Whole: sec.Whole},
		}

		rs.Minutes, rs.MinutesKey = cfg.SuiteMinutes(name, s), cfg.SuiteMinutesKey(name, s)

		out.Suites = append(out.Suites, rs)
	}

	return out
}

func tools(cfg *config.Config) []plugin.SpecTool {
	b := cfg.Concourse.Planner.Bootstrap

	pg := plugin.SpecTool{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Repository: b.Repository, Commit: b.Commit}
	if b.Release != nil {
		pg.Version, pg.SHA256, pg.Sums = b.Release.Version, b.Release.SHA256, b.Release.Sums
	}

	out := make([]plugin.SpecTool, 0, 2+len(cfg.Plugins))
	out = append(out, pg,
		plugin.SpecTool{Name: "bazelisk", Kind: plugin.ToolBazelisk, Version: b.Bazelisk.Version, SHA256: b.Bazelisk.SHA256})

	for _, p := range cfg.Plugins {
		t := plugin.SpecTool{Name: p.Name, Kind: p.Kind, Repository: p.Repository, Path: p.Path, Commit: p.Commit}
		if p.Release != nil {
			t.Version, t.SHA256, t.Sums = p.Release.Version, p.Release.SHA256, p.Release.Sums
		}

		if p.Zip != nil {
			t.Zip = &plugin.SpecZip{URL: p.Zip.URL, SHA256: p.Zip.SHA256}
		}

		out = append(out, t)
	}

	// the forges the config uses without an entry of its own, so the bootstrap fetches them
	// with the pipeline's read token
	for _, name := range cfg.ForgesUsed() {
		// an entry by its name, of any kind, has the plugins directory's file of that name
		if slices.ContainsFunc(cfg.Plugins, func(p config.Plugin) bool { return p.Name == name }) {
			continue
		}

		pin := cfg.ForgePin(name)
		sum, _ := pin.SHA256.For(config.LinuxAMD64)

		out = append(out, plugin.SpecTool{Name: name, Kind: config.KindForge, Repository: pin.Repository, Path: pin.Path, Version: pin.Version, SHA256: sum, Sums: pin.Sums})
	}

	return out
}

func asMap(v any) (map[string]any, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}

	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}

	return m, nil
}

// withPackages adds what the steps need installed, and the cached images that bring it, one build
// per key.
func withPackages(s *plugin.Spec, cfg *config.Config, include []byte) {
	if ps := cfg.Concourse.Planner.Packages; len(ps) > 0 {
		s.PlannerPackages = &plugin.SpecPackages{Names: packages.Names(ps), Install: packages.Script(ps)}
	}

	if i := cfg.Images; i.Registry != "" {
		s.Images = &plugin.SpecImages{Registry: i.Registry, Credentials: i.Credentials, PullCredentials: i.PullCredentials}
	}

	var bases map[string]packages.Base
	if len(include) > 0 {
		bases, _ = packages.Bases(include)
	}

	uses := packages.Uses(cfg, bases)
	built := map[string]bool{}

	for i, su := range s.Suites {
		ps := cfg.PackagesFor(su.Name)
		if len(ps) == 0 {
			continue
		}

		sp := &plugin.SpecPackages{Names: packages.Names(ps), Install: packages.Script(ps)}

		for _, u := range uses {
			if u.Suite != su.Name || u.Key == "" {
				continue
			}

			if sp.Images == nil {
				sp.Images = map[string]string{}
			}

			sp.Images[u.Image] = u.Key

			if !built[u.Key] {
				built[u.Key] = true
				s.Images.Builds = append(s.Images.Builds, plugin.SpecImageBuild{
					Key: u.Key, Base: u.Image, Digest: u.Base.Digest, Names: sp.Names, Install: sp.Install,
				})
			}
		}

		s.Suites[i].Packages = sp
	}

	if s.Images != nil {
		slices.SortFunc(s.Images.Builds, func(a, b plugin.SpecImageBuild) int { return strings.Compare(a.Key, b.Key) })
	}
}
