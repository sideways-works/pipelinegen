package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/spec"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errRunner = errors.New("runner")

func runPipeline(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c           common
		out, runner string
		plugins     string
		vars        bool
		gate        string
	)

	fs := flag.NewFlagSet("pipeline", flag.ContinueOnError)
	c.register(fs)
	pathVar(fs, &out, "out", "", "write the pipeline to this file (default stdout)", writesFile)
	pathVar(fs, &runner, "runner", "", "the runner plugin's executable (default <plugins>/<its name>)", readsFile)
	pathVar(fs, &plugins, "plugins", filepath.Join("tools", "plugins"), "the directory the bootstrap fetched the plugins into", reads)
	fs.BoolVar(&vars, "vars", false, "print the vars the pipeline references, one per line, instead of the pipeline")
	pathVar(fs, &gate, "gate", "", "in the pipeline's own job: write gate.json here, open only when the checkout's config is the default branch's newest, so builds that run side by side never set an older pipeline over a newer one", writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	if gate != "" {
		if newest, err := pipelineGate(ctx, cfg, c.root, gate, stdout); err != nil || !newest {
			return err
		}
	}

	// Listing the vars needs no Concourse, so a stand-in address lets them be listed before it is known.
	if vars && cfg.Concourse.URL == "" {
		cfg.Concourse.URL = varsOnlyURL
	}

	text, notices, err := pipelineText(ctx, cfg, c.root, runner, plugins)
	if err != nil {
		return err
	}

	if conf, ok := cfg.Runner(); ok {
		sayNotices(os.Stderr, conf.Name, notices)
	}

	if vars {
		text = []byte(strings.Join(pipelineVars(text), "\n") + "\n")
	}

	if out == "" {
		if _, err := stdout.Write(text); err != nil {
			return fmt.Errorf("write pipeline: %w", err)
		}

		return nil
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return fmt.Errorf("write pipeline: %w", err)
	}

	if err := os.WriteFile(out, text, 0o600); err != nil {
		return fmt.Errorf("write pipeline: %w", err)
	}

	return nil
}

// varsOnlyURL is in the reserved .invalid domain, which no pipeline is ever set from.
const varsOnlyURL = "https://concourse.invalid"

// varRef is a var a pipeline references: ((name)), or ((source:name)) for a
// var source's.
var varRef = regexp.MustCompile(`\(\(([^().\s][^()\s]*)\)\)`)

// pipelineVars leaves out the vars a step loads or an across sets (((.:name))): the rest are what
// whoever sets the pipeline has to pass.
func pipelineVars(text []byte) []string {
	var out []string

	for _, m := range varRef.FindAllSubmatch(text, -1) {
		if v := string(m[1]); !slices.Contains(out, v) {
			out = append(out, v)
		}
	}

	slices.Sort(out)

	return out
}

func pipelineText(ctx context.Context, cfg *config.Config, root, runner, plugins string) ([]byte, []string, error) {
	conf, ok := cfg.Runner()
	if !ok {
		return nil, nil, fmt.Errorf("%w: no runner plugin is configured: add one to plugins "+
			"(kind runner, for Concourse sideways-works/pipelinegen-runner-concourse)", errRunner)
	}

	if runner == "" {
		runner = filepath.Join(plugins, conf.Name)
	}

	s, err := spec.From(cfg)
	if err != nil {
		return nil, nil, err
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: %w", err)
	}

	work, err := os.MkdirTemp("", "pipelinegen-runner-")
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: %w", err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory; nothing to do when removing it fails

	p := plugin.Plugin{Name: conf.Name, Path: runner, Config: conf.Config}

	desc, err := p.Describe(ctx, work)
	if err != nil {
		return nil, nil, err
	}

	if err := runnerPlaces(conf.Name, desc); err != nil {
		return nil, nil, err
	}

	if err := runnerCan(cfg, conf.Name, desc); err != nil {
		return nil, nil, err
	}

	serialPlans(&s, desc)

	var ans plugin.PipelineAnswer
	if err := p.Call(ctx, plugin.CallPipeline, plugin.PipelineRequest{Request: p.NewRequest(work), Spec: s, Root: absRoot}, &ans); err != nil {
		return nil, nil, err
	}

	if len(ans.Files) != 1 {
		return nil, nil, fmt.Errorf("%w: %s answered %d files, not one pipeline", errRunner, conf.Name, len(ans.Files))
	}

	text, err := os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))) //nolint:gosec // inside the call's work directory
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errRunner, err)
	}

	return text, noticeLines(ans.Notices), nil
}

func sayNotices(w io.Writer, runner string, notices []string) {
	for _, n := range notices {
		warnf(w, "pipelinegen pipeline: runner %s: %s\n", runner, n)
	}
}

func noticeLines(notices []string) []string {
	var out []string

	for _, n := range notices {
		if line := strings.Join(strings.Fields(n), " "); line != "" && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}

	return out
}

const mockType = "mock"

func runnerBuildsConnectorRuns(cfg *config.Config, runner string, desc plugin.Description) error {
	if !cfg.ChangesFromConnector() {
		return nil
	}

	for _, name := range cfg.RunNames() {
		if cfg.Runs[name].Trigger.PullRequests == nil {
			continue
		}

		if !slices.Contains(desc.Capabilities, plugin.CapConnectorChanges) {
			return fmt.Errorf("%w: run %s takes changes from the %s connector, which runner %s %s can't build a pipeline for: "+
				"update the runner, or plan them locally with affected", errRunner, name, cfg.CodeConnector().Name, runner, desc.Version)
		}

		if slices.Contains(desc.ResourceTypes, mockType) && cfg.Concourse.Mock == config.MockMissing {
			return fmt.Errorf("%w: run %s hands each change on with Concourse's bundled mock resource type, and concourse.mock says "+
				"this Concourse lacks it: `fly -t <target> workers -d` lists each worker's types; without mock, see the runner's docs",
				errRunner, name)
		}
	}

	return nil
}

// runnerRunsContinuous refuses a continuous run the runner can't build: it would make a plain branch run of it.
func runnerRunsContinuous(cfg *config.Config, runner string, desc plugin.Description) error {
	if slices.Contains(desc.Capabilities, plugin.CapContinuous) {
		return nil
	}

	for _, name := range cfg.RunNames() {
		if cfg.Runs[name].Trigger.Continuous {
			return fmt.Errorf("%w: run %s is continuous, and runner %s %s can't build a continuous run: update the runner",
				errRunner, name, runner, desc.Version)
		}
	}

	return nil
}

// serialPlans asks a runner that can to run a continuous run's planning builds one at a time; one that
// can't runs them side by side, and each build records its round for the others to read.
func serialPlans(s *plugin.Spec, desc plugin.Description) {
	if !slices.Contains(desc.Capabilities, plugin.CapSerialJobs) {
		return
	}

	for i, r := range s.Runs {
		s.Runs[i].SerialPlan = r.Trigger.Kind == plugin.TriggerContinuous
	}
}

// runnerLocksPerBatch refuses a lock per batch the runner can't take so: it would hold it for the whole build.
func runnerLocksPerBatch(cfg *config.Config, runner string, desc plugin.Description) error {
	if slices.Contains(desc.Capabilities, plugin.CapLockPerBatch) {
		return nil
	}

	for _, name := range cfg.SuiteNames() {
		if cfg.Suites[name].Lock.PerBatch() {
			return fmt.Errorf("%w: suite %s takes its lock per batch, and runner %s %s can't: update the runner",
				errRunner, name, runner, desc.Version)
		}
	}

	return nil
}

func runnerCan(cfg *config.Config, name string, desc plugin.Description) error {
	for _, can := range []func(*config.Config, string, plugin.Description) error{
		runnerBuildsConnectorRuns, runnerHandsSSHKeys, runnerRunsContinuous, runnerLocksPerBatch, runnerWritesSteps,
	} {
		if err := can(cfg, name, desc); err != nil {
			return err
		}
	}

	return nil
}

// runnerWritesSteps refuses a driver-run suite the runner can't write tasks for: it would look for task
// files the suite doesn't have.
func runnerWritesSteps(cfg *config.Config, runner string, desc plugin.Description) error {
	if slices.Contains(desc.Capabilities, plugin.CapSteps) {
		return nil
	}

	for _, name := range cfg.SuiteNames() {
		if cfg.Suites[name].HasSteps() {
			return fmt.Errorf("%w: suite %s is run by drivers (steps), and runner %s %s can't write its tasks: update the runner",
				errRunner, name, runner, desc.Version)
		}
	}

	return nil
}

// A runner that can't read a placement of its own config would look for worker tags the spec doesn't
// carry and put every step on untagged workers.
func runnerPlaces(runner string, desc plugin.Description) error {
	if desc.Kind != plugin.KindRunner {
		return fmt.Errorf("%w: %s is a %s, not a runner", errRunner, runner, desc.Kind)
	}

	if slices.Contains(desc.Capabilities, plugin.CapPlacement) {
		return nil
	}

	return fmt.Errorf("%w: runner %s %s places steps by worker tags this pipelinegen no longer passes: "+
		"update the runner, which reads them from plugins.%s.config.%s", errRunner, runner, desc.Version, runner, config.PlacementKey)
}

func runnerHandsSSHKeys(cfg *config.Config, runner string, desc plugin.Description) error {
	if !cfg.UsesSSH() || slices.Contains(desc.Capabilities, plugin.CapSSHKeys) {
		return nil
	}

	return fmt.Errorf("%w: a remote is reached over ssh, and runner %s %s hands its steps no ssh key: "+
		"update the runner; affected, why and status reach it with your own ssh", errRunner, runner, desc.Version)
}

// A build of an older config change running beside a newer one's would set the older pipeline last.
// It compares the config's files with the branch's tip, so a shallow checkout is enough.
func pipelineGate(ctx context.Context, cfg *config.Config, root, gate string, stdout io.Writer) (bool, error) {
	tip, err := runs.FetchTip(ctx, root, cfg.Concourse.Branch, readAuth(ctx, cfg, root))
	if err != nil {
		return false, fmt.Errorf("pipeline: the tip of %s: %w", cfg.Concourse.Branch, err)
	}

	files := []string{config.FileName}
	if cfg.Concourse.Include != "" {
		files = append(files, cfg.Concourse.Include)
	}

	_, diff := gitx.Repo{Dir: root}.Git(ctx, append([]string{"diff", "--quiet", "HEAD", tip, "--"}, files...)...)
	newest := diff == nil

	if err := os.MkdirAll(gate, 0o750); err != nil {
		return false, fmt.Errorf("gate: %w", err)
	}

	if err := writeGate(filepath.Join(gate, "gate.json"), newest); err != nil {
		return false, err
	}

	if !newest {
		return false, say(stdout, "superseded by %s: its config is newer, and its build sets the pipeline\n", short12(tip))
	}

	return true, nil
}
