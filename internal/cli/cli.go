// Package cli holds pipelinegen's commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

// errUsage marks a command line pipelinegen can't read; it exits with 2.
var errUsage = errors.New("usage")

type command struct {
	name, summary string
	run           func(ctx context.Context, args []string, stdout io.Writer) error
}

func commands() []command {
	return []command{
		{"slices", "list the slices of every suite, or of one, with their sources", runSlices},
		{"affected", "print the slices a change reaches", metered("affected", runAffected)},
		{"check", "check that every file is covered by a slice or declared untested: [--staged]", runCheck},
		{"pipeline", "write the repository's pipeline from its config", runPipeline},
		{"plan", "plan the change a planning job's trigger stands for", metered("plan", runPlan)},
		{"unplanned", "list a pull request run's heads its planning job missed, and planned ones no suite took up", runUnplanned},
		{"tree", "write a suite job's tree under test and its gates", runTree},
		{"batches", "deal a suite's selected slices into batches", runBatches},
		{"learn", "write the stub deps repository a fresh checkout needs: --stub", runLearn},
		{"report", "keep a run's state and show it on GitHub: start, slice, batch-end, suite, finish, collect", runReport},
		{sliceCommand, "report a slice of the running batch: start <name>, end <name>", runSlice},
		{"lock", "take or free a slot of a lock between runs: acquire <name>, release <name>", runLock},
		{"unlock", "free a lock's slot by hand: <name> [<slot>]", runUnlock},
		{"end", "end a run of a change by hand, such as a continuous run's round whose build is gone: --run <run> --commit <commit> [--change <change>]", runEnd},
		{"also-to", "post a pull request run's comment to more pull requests", runAlsoTo},
		{"lists", "attach a run's recordings to a branch commit, or show a commit's lists: attach, show", runLists},
		{"durations", "record a run's test reports into a suite's durations file", runDurations},
		{"why", "say why a slice ran for a change, or why it didn't", runWhy},
		{"status", "say what the records hold about a commit's runs, one line each; exit 0 passed, 1 failed, 2 pending, 3 no run: <commit|branch>", runStatus},
		{"try", "plan each of the last commits under the current config, as if pipelinegen had run all along: --last <n>", runTry},
		{"run", "plan a change and run the slices it selects on this machine, as a pull request's run would; it plans and runs commits, so uncommitted changes are neither planned nor run: --base <commit> | --all", metered("run", runLocal)},
		{"record", "record a suite's slices at HEAD on this machine, into a local store affected and why read", runRecord},
		{"seed", "put HEAD's local recordings into the shared store as its lists, the base of the branch's first runs: [--dry-run] [--replace]", runSeed},
		{"baseline", "declare HEAD good by hand as a run's base, so its first runs need not run everything: --run <run> [--dry-run]", runBaseline},
		{"version", "print pipelinegen's version", runVersion},
		{"plugin", "check a plugin against the protocol: test <executable> [--tree <dir>]", runPlugin},
		{"plugins", "inspect the configured sbom and correlator plugins: claims", runPlugins},
		{"git-auth", "print shell lines that have git authenticate with the token in a variable: <ENV_NAME>", runGitAuth},
		{"help", "list the commands, or describe one with its flags: [<command>]", runHelp},
	}
}

// Version is the release's tag, set by the release build through cmd/pipelinegen, or "dev".
var Version = "dev"

func runVersion(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	return say(stdout, "%s\n", Version)
}

// Run runs the command named by args[0] and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)

		return 2
	}

	if slices.Contains(helpFlags, args[0]) {
		warnf(stdout, "%s", commandList())

		return 0
	}

	for _, c := range commands() {
		if c.name != args[0] {
			continue
		}

		if asksHelp(args[1:]) {
			text, _ := commandHelp(ctx, c.name)
			warnf(stdout, "%s", text)

			return 0
		}

		err := runLimited(ctx, c.name, func(ctx context.Context) error { return c.run(ctx, args[1:], stdout) }, stderr)

		// The ssh key a step wrote and the change it checked out go whatever happened.
		closeSSH()
		closeScratch()

		if code, ok := errors.AsType[exitCodeError](err); ok {
			return int(code)
		}

		switch {
		case err == nil:
			return 0
		case errors.Is(err, flag.ErrHelp):
			return 0
		case errors.Is(err, errUsage):
			warnf(stderr, "pipelinegen %s: %v\n", c.name, err)

			return 2
		case errors.Is(err, report.ErrUnrecorded):
			warnf(stderr, "pipelinegen %s: %v\n", c.name, err)

			return ExitUnrecorded
		default:
			warnf(stderr, "pipelinegen %s: %v\n", c.name, err)

			return 1
		}
	}

	usage(stderr)

	return 2
}

// ExitUnrecorded is the exit code of a step whose records could not be written after every retry:
// a pipeline never lets such a step pass.
const ExitUnrecorded = 4

func usage(w io.Writer) {
	warnf(w, "%s", commandList())
}

// warnf drops a failed write's error on purpose: stderr leaves nowhere to report it.
func warnf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func emit(w io.Writer, b *bytes.Buffer) error {
	if _, err := w.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

type common struct {
	root   string
	config string
	// history marks a suite job's step: the tested commit's config may be older than this tool, so it is
	// read as history (see config.ParseHistory).
	history bool
	// spellings says the config's old spellings too: check and plan do; other steps would repeat them in every log.
	spellings bool
}

func (c *common) register(fs *flag.FlagSet) {
	pathVar(fs, &c.root, "root", ".", "the repository's root", reads)
	pathVar(fs, &c.config, "config", "", "the config file (default <root>/"+config.FileName+")", readsFile)
}

func (c *common) load() (*config.Config, error) {
	p := c.config
	if p == "" {
		env, ok, err := configFromEnv()
		if err != nil {
			return nil, err
		}

		p = filepath.Join(c.root, config.FileName)
		if ok {
			p = env
		}
	}

	var (
		cfg *config.Config
		err error
	)

	if c.history {
		var data []byte
		if data, err = os.ReadFile(p); err == nil { //nolint:gosec // the config the caller names
			cfg, err = config.ParseHistory(data)
		}
	} else {
		cfg, err = config.Load(p)
	}

	if err != nil {
		return nil, fmt.Errorf("config %s: %w", p, err)
	}

	warnings := cfg.Warnings
	if c.spellings {
		warnings = append(slices.Clone(warnings), cfg.OldSpellings...)
	}

	for _, w := range warnings {
		warnf(os.Stderr, "pipelinegen: config %s: %s\n", p, w)
	}

	return cfg, nil
}

func withOrigin(ctx context.Context, cfg *config.Config, root string) {
	if !cfg.Reports() && cfg.Git.Remote == "" && cfg.Git.Origin == "" {
		cfg.Git.Origin = originOf(ctx, root)
	}
}

func originOf(ctx context.Context, root string) string {
	out, err := gitx.Repo{Dir: root}.Git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}

	remote := strings.TrimSpace(out)
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.User != nil {
		u.User = nil
		remote = u.String()
	}

	return remote
}

func (c *common) bazel(cfg *config.Config) *bazel.Client {
	return &bazel.Client{
		Bazelisk:     bazeliskFor(cfg, c.root),
		Dir:          filepath.Join(c.root, cfg.Bazel.Workspace),
		StartupFlags: startupFlags(cfg),
		Env:          childEnv(cfg),
	}
}

// pipelineIdle is long enough for a plan's pauses between Bazel calls, short enough that a killed
// step's server doesn't hold a kept container's memory for hours.
const pipelineIdle = "--max_idle_secs=600"

func startupFlags(cfg *config.Config) []string {
	flags := slices.Clone(cfg.Bazel.StartupFlags)

	if secret.InPipeline() && !hasFlag(flags, "--max_idle_secs") {
		flags = append(flags, pipelineIdle)
	}

	return flags
}

func hasFlag(flags []string, name string) bool {
	return slices.ContainsFunc(flags, func(f string) bool { return f == name || strings.HasPrefix(f, name+"=") })
}

const bazelDir = "bazel"

// A CI system gives each build a fresh home, where Bazel's output would die with the container, and
// Bazel derives the output base from the workspace path, which may change between builds.
func cachedStartupFlags(cfg *config.Config, cache string) []string {
	flags := startupFlags(cfg)

	if !secret.InPipeline() || cache == "" || hasFlag(flags, "--output_user_root") || hasFlag(flags, "--output_base") {
		return flags
	}

	dir := absOr(filepath.Join(cache, bazelDir))

	return append(flags, "--output_user_root="+filepath.Join(dir, "root"), "--output_base="+filepath.Join(dir, "base"))
}

func cachedBazelEnv(cfg *config.Config, cache string) []string {
	env := childEnv(cfg)

	if secret.InPipeline() && cache != "" && os.Getenv("BAZELISK_HOME") == "" {
		env = append(env, "BAZELISK_HOME="+absOr(filepath.Join(cache, bazelDir, "bazelisk")))
	}

	return env
}

// childEnv is for programs run on a tree a proposed change may have made: no token, ssh key or
// variable of the tool's, only the planner's declared git token for Bazel's private module fetches.
func childEnv(cfg *config.Config) []string {
	return secret.ChildEnv(cfg.Concourse.Planner.GitToken)
}

func parse(ctx context.Context, fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)

	// asked to describe itself, a command hands over its flags and stops.
	if seen, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet); ok {
		*seen = append(*seen, fs)

		return flag.ErrHelp
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return flag.ErrHelp
		}

		return fmt.Errorf("%w: %w", errUsage, err)
	}

	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected %q", errUsage, fs.Arg(0))
	}

	return absolutePaths(fs)
}

// git, Bazel and plugins run in other directories, where a relative path means something else.
func absolutePaths(fs *flag.FlagSet) error {
	var err error

	fs.VisitAll(func(f *flag.Flag) {
		v := f.Value.String()
		if _, isPath := f.Value.(*pathValue); err != nil || !isPath || v == "" || v == "-" || filepath.IsAbs(v) {
			return
		}

		abs, aerr := filepath.Abs(v)
		if aerr == nil {
			aerr = f.Value.Set(abs)
		}

		if aerr != nil {
			err = fmt.Errorf("--%s %s: %w", f.Name, v, aerr)
		}
	})

	return err
}

type describeKey struct{}

// CommandDoc describes one command for the reference.
type CommandDoc struct {
	Name    string
	Summary string
	// Flags are the command's own, or for a command with subcommands every flag any of them takes.
	Flags []FlagDoc
	// Subcommands describe the subcommands, each with its own flags; Name is the subcommand's alone.
	Subcommands []CommandDoc
}

// FlagDoc describes one flag.
type FlagDoc struct {
	Name    string
	Usage   string
	Default string
	// Use is what the command does with the path the flag names; none for a flag that names no path.
	Use toolcalls.Use
	// Bool flags take no value after a space.
	Bool bool
	// Plain marks a string flag defined as neither pathVar nor textVar, which no flag may be.
	Plain bool
}

func flagDocs(sets []*flag.FlagSet) []FlagDoc {
	var out []FlagDoc

	seen := map[string]bool{}

	for _, fs := range sets {
		fs.VisitAll(func(f *flag.Flag) {
			if !seen[f.Name] {
				seen[f.Name] = true
				d := FlagDoc{Name: f.Name, Usage: f.Usage, Default: f.DefValue, Plain: fmt.Sprintf("%T", f.Value) == "*flag.stringValue"}

				if p, ok := f.Value.(*pathValue); ok {
					d.Use = p.use
				}

				if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
					d.Bool = b.IsBoolFlag()
				}

				out = append(out, d)
			}
		})
	}

	return out
}

// Commands lists every command with its flags and subcommands, without running any.
func Commands() []CommandDoc {
	return commandDocs(context.Background())
}

func commandDocs(parent context.Context) []CommandDoc {
	docs := make([]CommandDoc, 0, len(commands()))

	for _, c := range commands() {
		var (
			seen []*flag.FlagSet
			subs []CommandDoc
		)

		ctx := context.WithValue(parent, describeKey{}, &seen)
		ctx = context.WithValue(ctx, subDocsKey{}, &subs)

		if err := c.run(ctx, nil, io.Discard); err != nil && !errors.Is(err, flag.ErrHelp) {
			continue
		}

		docs = append(docs, CommandDoc{Name: c.name, Summary: c.summary, Flags: flagDocs(seen), Subcommands: subs})
	}

	return docs
}

func pickSuites(cfg *config.Config, name string) ([]string, error) {
	if name == "" {
		return cfg.SuiteNames(), nil
	}

	if _, ok := cfg.Suites[name]; !ok {
		return nil, fmt.Errorf("%w: no suite %q", errUsage, name)
	}

	return []string{name}, nil
}

type suiteSlices struct {
	Suite  string        `json:"suite"`
	Slices []bazel.Slice `json:"slices"`
}

func runSlices(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c      common
		suite  string
		asJSON bool
	)

	fs := flag.NewFlagSet("slices", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &suite, "suite", "", "only this suite")
	fs.BoolVar(&asJSON, "json", false, "print JSON")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	names, err := pickSuites(cfg, suite)
	if err != nil {
		return err
	}

	bz := c.bazel(cfg)
	out := make([]suiteSlices, 0, len(names))

	for _, name := range names {
		var (
			s   []bazel.Slice
			err error
		)

		if cfg.Suites[name].Implicit() {
			s, err = affected.TestFileSlices(ctx, filepath.Join(c.root, cfg.Bazel.Workspace), cfg.Suites[name])
		} else {
			s, err = bz.Slices(ctx, cfg.Suites[name].Tag)
		}

		if err != nil {
			return fmt.Errorf("suite %s: %w", name, err)
		}

		out = append(out, suiteSlices{Suite: name, Slices: s})
	}

	var b bytes.Buffer

	if asJSON {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encode: %w", err)
		}

		return emit(stdout, &b)
	}

	for _, s := range out {
		for _, sl := range s.Slices {
			fmt.Fprintf(&b, "%s %s\n", s.Suite, shownName(sl))

			for _, src := range sl.Sources {
				fmt.Fprintf(&b, "  %s\n", src)
			}
		}
	}

	return emit(stdout, &b)
}

// metered reports progress on stderr once a minute after the first, since a slow plan and a stuck one
// look alike from outside.
func metered(name string, run func(context.Context, []string, io.Writer) error) func(context.Context, []string, io.Writer) error {
	return func(ctx context.Context, args []string, stdout io.Writer) error {
		ctx, stop := progress.Start(ctx, os.Stderr, "pipelinegen "+name)
		defer stop()

		return run(ctx, args, stdout)
	}
}
