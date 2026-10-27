// Package spec turns a config into the spec a runner plugin gets: the runs
// and suites as the core reads them, with defaults applied and per-run
// values worked out, so a runner needs no copy of the config's rules.
package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrSpec wraps a config a spec can't be made from.
var ErrSpec = errors.New("spec")

// From makes the spec of a config.
func From(cfg *config.Config) (plugin.Spec, error) {
	// only a pipeline links to Concourse: no other command needs its
	// address, so a missing one fails here and nowhere else
	if len(cfg.Runs) > 0 && cfg.Concourse.URL == "" {
		return plugin.Spec{}, fmt.Errorf("%w: concourse needs its url: concourse.url, or %s in the environment", ErrSpec, config.EnvConcourseURL)
	}

	// a config may declare runs before it names their remote and CI
	// system; only a pipeline needs them
	if err := cfg.ValidatePipeline(); err != nil {
		return plugin.Spec{}, fmt.Errorf("%w: %w", ErrSpec, err)
	}

	ci, err := asMap(cfg.Concourse)
	if err != nil {
		return plugin.Spec{}, err
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
		Limits: plugin.SpecLimits{
			Plan: cfg.Limits.PlanMinutes(), Steps: cfg.Limits.StepMinutes(), LockWait: plugin.LockWaitMinutes,
		},
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

	if cfg.Concourse.Include != "" {
		s.ConfigFiles = append(s.ConfigFiles, cfg.Concourse.Include)
	}

	for _, name := range cfg.SuiteNames() {
		s.Suites = append(s.Suites, suite(cfg, name))
	}

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
		SerialGroup: su.RunOn.SerialGroup, MaxInFlight: su.RunOn.MaxInFlight,
		Parallel: parallel, Privileged: su.Privileged,
	}

	if l := su.Lock; l != nil {
		out.Lock = &plugin.SpecLock{Name: l.Name, Minutes: cfg.SuiteMinutes("", name), Slots: l.Slots}
		if l.PerBatch() {
			out.Lock.Per = config.LockPerBatch
		}
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

		if cc := cfg.CodeConnector(); cc != nil && cfg.ChangesFromConnector() {
			tr.Connector = cc.Name

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
		if cfg.Suites[s].Lock != nil {
			rs.LockMinutes = rs.Minutes
		}

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
		t := plugin.SpecTool{Name: p.Name, Kind: p.Kind, Repository: p.Repository, Commit: p.Commit}
		if p.Release != nil {
			t.Version, t.SHA256, t.Sums = p.Release.Version, p.Release.SHA256, p.Release.Sums
		}

		out = append(out, t)
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
