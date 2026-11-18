package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Command is the built-in driver, a suite's own commands: prepare runs `run`; a set-up starts `start`,
// waits for `ready`, exports `export`, stops with `stop`; a run step runs `each` per slice or `all` per bundle.
type Command struct {
	Env    []string
	Log    io.Writer
	Client *http.Client
}

// ErrCommand is a command step the config or its command got wrong.
var ErrCommand = errors.New("command")

func (c *Command) Name() string { return "command" }

func CommandDescription() plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "command", Version: "builtin",
		Verbs:     slices.Clone(plugin.DriverVerbs),
		Routes:    &plugin.Routes{Files: []string{"**"}, Kinds: []string{"*"}},
		Apart:     true,
		Results:   plugin.ResultsExit,
		Validates: true,
		Settings: map[string]string{
			keyRun: plugin.SettingList, keyStart: plugin.SettingList, keyStop: plugin.SettingList,
			keyEach: plugin.SettingList, keyAll: plugin.SettingList,
			keyDir: plugin.SettingString, keyEnv: plugin.SettingMap, keyReady: plugin.SettingMap,
			keyExport: plugin.SettingMap,
		},
	}
}

func (c *Command) Describe(context.Context) (plugin.Description, error) {
	return CommandDescription(), nil
}

type commandConf struct {
	Run    []string          `json:"run"`
	Start  []string          `json:"start"`
	Stop   []string          `json:"stop"`
	Each   []string          `json:"each"`
	All    []string          `json:"all"`
	Dir    string            `json:"dir"`
	Env    map[string]string `json:"env"`
	Ready  readyConf         `json:"ready"`
	Export map[string]string `json:"export"`
}

func (*commandConf) SettingNoun() string { return "the command driver's settings" }

type readyConf struct {
	HTTP    string `json:"http"`
	File    string `json:"file"`
	TCP     string `json:"tcp"`
	Seconds int    `json:"seconds"`
}

func (*readyConf) SettingNoun() string { return "ready" }

const (
	keyRun    = "run"
	keyStart  = "start"
	keyStop   = "stop"
	keyEach   = "each"
	keyAll    = "all"
	keyDir    = "dir"
	keyEnv    = "env"
	keyReady  = "ready"
	keyExport = "export"
)

// verbKeys are the keys each verb of the command driver reads: a key of
// another verb would be ignored.
var verbKeys = map[string][]string{
	plugin.CallPrepare: {keyRun, keyDir, keyEnv},
	plugin.CallSetup:   {keyRun, keyStart, keyStop, keyReady, keyExport, keyDir, keyEnv},
	plugin.CallRun:     {keyEach, keyAll, keyDir, keyEnv},
}

var stepNames = map[string]string{plugin.CallPrepare: "a prepare step", plugin.CallSetup: "a set-up", plugin.CallRun: "a run step"}

// varName is what a variable's name may be: A=B would set A.
var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateCommand checks a command step's settings for its verb as the
// verb reads them, without the machine.
func ValidateCommand(verb string, settings map[string]any) error {
	_, err := commandConfFor(verb, settings)

	return err
}

func confOf(req plugin.DriverRequest, verb string) (commandConf, error) {
	c, err := commandConfFor(verb, req.Config)
	if err != nil {
		return c, fmt.Errorf("%w: %w", ErrCommand, err)
	}

	return c, nil
}

// commandConfFor reads a command step's settings for a verb, refusing any
// the verb would ignore or misread.
func commandConfFor(verb string, conf map[string]any) (commandConf, error) {
	var c commandConf

	if err := plugin.DecodeSettings(conf, &c); err != nil {
		return c, err
	}

	keys := verbKeys[verb]

	for _, k := range slices.Sorted(maps.Keys(conf)) {
		if !slices.Contains(keys, k) {
			return c, &plugin.SettingError{Place: k, Problem: fmt.Sprintf("%s doesn't read it; it reads %s", stepNames[verb], strings.Join(keys, ", "))}
		}
	}

	ready, _ := conf[keyReady].(map[string]any) //nolint:errcheck // a ready of another type is refused above
	clean := filepath.ToSlash(filepath.Clean(c.Dir))

	switch {
	case verb == plugin.CallPrepare && len(c.Run) == 0:
		return c, &plugin.SettingError{Problem: "a prepare step needs run"}
	case verb == plugin.CallRun && len(c.Each) == 0 && len(c.All) == 0:
		return c, &plugin.SettingError{Problem: "a run step needs each or all"}
	case verb == plugin.CallSetup && len(c.Run) == 0 && len(c.Start) == 0 && len(c.Export) == 0:
		return c, &plugin.SettingError{Problem: "a set-up needs run, start or export"}
	case len(c.Stop) > 0 && len(c.Start) == 0:
		return c, &plugin.SettingError{Place: keyStop, Problem: "stops what start started, and the set-up has no start"}
	case ready != nil && len(c.Start) == 0:
		return c, &plugin.SettingError{Place: keyReady, Problem: "waits for what start started, and the set-up has no start"}
	case filepath.IsAbs(c.Dir) || clean == ".." || strings.HasPrefix(clean, "../"):
		return c, &plugin.SettingError{Place: keyDir, Problem: strconv.Quote(c.Dir) + " is no directory inside the tree"}
	}

	if err := readySettings(c.Ready, ready); err != nil {
		return c, err
	}

	for _, set := range []struct {
		key  string
		vars map[string]string
	}{{keyEnv, c.Env}, {keyExport, c.Export}} {
		for _, k := range slices.Sorted(maps.Keys(set.vars)) {
			if !varName.MatchString(k) {
				return c, &plugin.SettingError{Place: fmt.Sprintf("%s[%q]", set.key, k), Problem: strconv.Quote(k) + " is no variable's name"}
			}
		}
	}

	return c, nil
}

func readySettings(r readyConf, given map[string]any) error {
	if given == nil {
		return nil
	}

	var ways []string

	for _, w := range []struct{ key, val string }{{"http", r.HTTP}, {"tcp", r.TCP}, {"file", r.File}} {
		if w.val != "" {
			ways = append(ways, w.key)
		}
	}

	_, seconds := given["seconds"]

	switch {
	case len(ways) == 0:
		return &plugin.SettingError{Place: keyReady, Problem: "names nothing to wait for; give one of http, tcp and file"}
	case len(ways) > 1:
		return &plugin.SettingError{Place: keyReady, Problem: "names " + strings.Join(ways, " and ") + "; it waits for one of them"}
	case seconds && r.Seconds < 1:
		return &plugin.SettingError{Place: "ready.seconds", Problem: fmt.Sprintf("%d is less than 1; leave it out for two minutes", r.Seconds)}
	}

	return nil
}

// fill puts the placeholders' values into a command's arguments; an argument
// that is exactly {mark} becomes the marks command's arguments.
func fill(argv []string, vals map[string]string, mark []string) []string {
	var out []string

	for _, a := range argv {
		if a == "{mark}" && mark != nil {
			out = append(out, mark...)

			continue
		}

		for k, v := range vals {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}

		out = append(out, a)
	}

	return out
}

func (c *Command) cmd(ctx context.Context, req plugin.DriverRequest, conf commandConf, argv []string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: no command", ErrCommand)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the suite's own command, from the trusted config
	cmd.Dir = filepath.Join(req.Tree, conf.Dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	env := withMarks(c.Env, req)
	for _, k := range slices.Sorted(maps.Keys(conf.Env)) {
		env = append(env, k+"="+fill([]string{conf.Env[k]}, common(req), nil)[0])
	}

	cmd.Env = env

	return cmd, nil
}

func common(req plugin.DriverRequest) map[string]string {
	return map[string]string{"source": req.Tree, "prepared": req.Prepared, "work": req.Work, "cache": req.Cache}
}

func (c *Command) Prepare(ctx context.Context, req plugin.DriverRequest) error {
	conf, err := confOf(req, plugin.CallPrepare)
	if err != nil {
		return err
	}

	return c.prepare(ctx, req, conf)
}

func (c *Command) prepare(ctx context.Context, req plugin.DriverRequest, conf commandConf) error {
	cmd, err := c.cmd(ctx, req, conf, fill(conf.Run, common(req), nil))
	if err != nil {
		return err
	}

	log := newTailWriter(c.Log, "command prepare: ")
	cmd.Stdout, cmd.Stderr = log, log

	err = cmd.Run()

	log.Close()

	if err != nil {
		return fmt.Errorf("%w: prepare %s: %w", ErrCommand, strings.Join(cmd.Args, " "), err)
	}

	return nil
}

// Run runs `all` once for the request's slices when the step bundles and
// has it, or else each slice by `each` (or by `all` given that one slice).
func (c *Command) Run(ctx context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error) {
	conf, err := confOf(req, plugin.CallRun)
	if err != nil {
		return "", err
	}

	log := newTailWriter(c.Log, "command run: ")
	defer log.Close()

	if len(conf.All) > 0 && (req.Batch || len(conf.Each) == 0) {
		return log.Tail(), c.runAll(ctx, req, conf, log, event)
	}

	if req.Batch && len(req.Slices) > 1 {
		sayf(c.Log, "command run: the step has no all, so the %d slices of its bundle run by each, one after another, as without batch\n", len(req.Slices))
	}

	for _, sl := range req.Slices {
		if err := c.runEach(ctx, req, conf, sl, log, event); err != nil {
			return log.Tail(), err
		}
	}

	return log.Tail(), nil
}

func sliceVals(req plugin.DriverRequest, sl plugin.DriverSlice, result string) map[string]string {
	v := common(req)
	v["name"] = sl.Name
	v["tests"] = strings.Join(sl.Tests, ",")
	v["result"] = result
	v["extras"] = sl.Extras
	v["files"] = sl.Files
	v["recording"] = sl.Recording
	v["label"] = sl.Label
	v["target"] = sl.Target
	v["source"] = filepath.Join(req.Tree, firstOr(sl.Sources, sl.Name))

	return v
}

func firstOr(list []string, or string) string {
	if len(list) > 0 {
		return list[0]
	}

	return or
}

func (c *Command) runEach(ctx context.Context, req plugin.DriverRequest, conf commandConf, sl plugin.DriverSlice, log *tailWriter, event func(plugin.DriverEvent)) error {
	dir := filepath.Join(req.Work, "command-results")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrCommand, err)
	}

	result := filepath.Join(dir, strconv.Itoa(int(time.Now().UnixNano()))+".json")

	var mark []string
	if req.Marks != nil {
		mark = req.Marks.Command
	}

	cmd, err := c.cmd(ctx, req, conf, fill(conf.Each, sliceVals(req, sl, result), mark))
	if err != nil {
		return err
	}

	cmd.Stdout, cmd.Stderr = log, log

	// in a marked bundle, an each command that doesn't mark itself is
	// marked around: its slice's edges are the command's own
	around := req.Marks != nil && !slices.ContainsFunc(conf.Each, func(a string) bool { return strings.Contains(a, "{mark}") })

	if around {
		if err := c.mark(ctx, req, plugin.MarkStart, sl.Name, log); err != nil {
			return err
		}
	}

	start := time.Now()
	runErr := cmd.Run()
	took := time.Since(start).Seconds()

	if around {
		if err := c.mark(ctx, req, plugin.MarkStop, sl.Name, log); err != nil {
			return err
		}
	}

	ev, ok := readResult(result, sl.Name)
	if !ok {
		var exit *exec.ExitError

		switch {
		case runErr == nil:
			ev = plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true}
		case errors.As(runErr, &exit):
			ev = plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Failures: []plugin.Failure{{Test: sl.Name, Tail: log.Tail()}}}
		default:
			// it never ran, or was killed: no result
			return fmt.Errorf("%w: %s: %w", ErrCommand, sl.Name, runErr)
		}
	}

	ev.Seconds = took
	event(ev)

	return nil
}

func (c *Command) mark(ctx context.Context, req plugin.DriverRequest, what, slice string, log io.Writer) error {
	argv := append(slices.Clone(req.Marks.Command), what, slice)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the tool's own command, from the request
	cmd.Env = withMarks(c.Env, req)
	cmd.Stdout, cmd.Stderr = log, log

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: mark %s %s: %w", ErrCommand, what, slice, err)
	}

	return nil
}

// runAll runs `all` for the bundle: it reads the slices from {slices} and
// writes {results}/<name>.json for each.
func (c *Command) runAll(ctx context.Context, req plugin.DriverRequest, conf commandConf, log *tailWriter, event func(plugin.DriverEvent)) error {
	dir := filepath.Join(req.Work, "command-bundle")
	if err := freshDir(dir); err != nil {
		return err
	}

	list := filepath.Join(dir, "slices.json")
	results := filepath.Join(dir, "results")

	data, err := json.Marshal(req.Slices)
	if err == nil {
		err = os.WriteFile(list, data, 0o600)
	}

	if err == nil {
		err = os.MkdirAll(results, 0o750)
	}

	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommand, err)
	}

	vals := common(req)
	vals["slices"], vals["results"] = list, results

	var mark []string
	if req.Marks != nil {
		mark = req.Marks.Command
	}

	cmd, err := c.cmd(ctx, req, conf, fill(conf.All, vals, mark))
	if err != nil {
		return err
	}

	cmd.Stdout, cmd.Stderr = log, log
	runErr := cmd.Run()

	// a slice without its result file gets no end: the tool runs it again
	// on its own
	for _, sl := range req.Slices {
		if ev, ok := readResult(filepath.Join(results, filepath.FromSlash(sl.Name)+".json"), sl.Name); ok {
			event(ev)
		}
	}

	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return fmt.Errorf("%w: all: %w", ErrCommand, runErr)
	}

	return nil
}

func readResult(path, slice string) (plugin.DriverEvent, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // the command's own result file
	if err != nil {
		return plugin.DriverEvent{}, false
	}

	var r struct {
		Passed   bool             `json:"passed"`
		Tests    *int             `json:"tests"`
		Failures []plugin.Failure `json:"failures"`
		// Files are named in the slice's {files} directory.
		Files []plugin.File `json:"files"`
	}

	if json.Unmarshal(data, &r) != nil {
		return plugin.DriverEvent{}, false
	}

	return plugin.DriverEvent{Slice: slice, Event: plugin.EventEnd, Passed: r.Passed && len(r.Failures) == 0, Tests: r.Tests, Failures: r.Failures, Files: r.Files}, true
}

// Setup runs `run` to its end, when given, then starts `start` and waits
// until it is ready. A set-up without `start` only runs.
func (c *Command) Setup(ctx context.Context, req plugin.DriverRequest) (Running, error) { //nolint:ireturn // a started process, or nothing to stop
	conf, err := confOf(req, plugin.CallSetup)
	if err != nil {
		return nil, err
	}

	if len(conf.Run) > 0 {
		if err := c.prepare(ctx, req, conf); err != nil {
			return nil, err
		}
	}

	exports := map[string]string{}
	for k, v := range conf.Export {
		exports[k] = fill([]string{v}, common(req), nil)[0]
	}

	if len(conf.Start) == 0 {
		return ran{env: exports}, nil
	}

	// the set-up runs until stopped, not until this call's context ends
	cmd, err := c.cmd(context.WithoutCancel(ctx), req, conf, fill(conf.Start, common(req), nil))
	if err != nil {
		return nil, err
	}

	log := newTailWriter(c.Log, "command setup: ")
	cmd.Stdout, cmd.Stderr = log, log

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: setup %s: %w", ErrCommand, strings.Join(cmd.Args, " "), err)
	}

	r := &commandSetup{c: c, req: req, conf: conf, cmd: cmd, log: log, exited: make(chan error, 1)}
	go func() { r.exited <- cmd.Wait() }()

	if err := r.waitReady(ctx); err != nil {
		return nil, errors.Join(err, r.Stop(ctx))
	}

	r.env = exports

	return r, nil
}

type ran struct{ env map[string]string }

func (r ran) Env() map[string]string     { return r.env }
func (r ran) Stop(context.Context) error { return nil }

type commandSetup struct {
	c      *Command
	req    plugin.DriverRequest
	conf   commandConf
	cmd    *exec.Cmd
	log    *tailWriter
	env    map[string]string
	exited chan error
	err    error
	done   bool
}

func (r *commandSetup) Env() map[string]string { return r.env }

const readyPoll = 200 * time.Millisecond

func (r *commandSetup) waitReady(ctx context.Context) error {
	ready := r.conf.Ready
	limit := time.Duration(max(ready.Seconds, 0)) * time.Second

	if limit == 0 {
		limit = 2 * time.Minute
	}

	deadline := time.Now().Add(limit)

	client := r.c.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}

	for {
		if r.isReady(ctx, client) {
			return nil
		}

		select {
		case err := <-r.exited:
			r.exited <- err

			if err == nil {
				return fmt.Errorf("%w: the set-up stopped before it was ready (it exited 0): %s", ErrCommand, r.log.Tail())
			}

			return fmt.Errorf("%w: the set-up stopped before it was ready (%w): %s", ErrCommand, err, r.log.Tail())
		case <-ctx.Done():
			return fmt.Errorf("%w: setup: %w", ErrCommand, ctx.Err())
		case <-time.After(readyPoll):
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w: the set-up was not ready within %s: %s", ErrCommand, limit, r.log.Tail())
		}
	}
}

func (r *commandSetup) isReady(ctx context.Context, client *http.Client) bool {
	ready := r.conf.Ready
	vals := common(r.req)

	switch {
	case ready.HTTP != "":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fill([]string{ready.HTTP}, vals, nil)[0], nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode < http.StatusBadRequest
	case ready.TCP != "":
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", ready.TCP)
		if err != nil {
			return false
		}

		_ = conn.Close() //nolint:errcheck // only that it answered counts

		return true
	case ready.File != "":
		p := fill([]string{ready.File}, vals, nil)[0]
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.req.Tree, r.conf.Dir, p)
		}

		_, err := os.Stat(p)

		return err == nil
	default:
		// nothing to wait for: started is ready
		return true
	}
}

// Stop runs `stop` when given (bounded), then TERM and KILL to the
// set-up's process group.
func (r *commandSetup) Stop(ctx context.Context) error {
	if r.done {
		return r.err
	}

	r.done = true

	if len(r.conf.Stop) > 0 {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)

		if cmd, err := r.c.cmd(sctx, r.req, r.conf, fill(r.conf.Stop, common(r.req), nil)); err == nil {
			cmd.Stdout, cmd.Stderr = r.log, r.log
			if err := cmd.Run(); err != nil {
				r.err = fmt.Errorf("%w: stop %s: %w", ErrCommand, strings.Join(cmd.Args, " "), err)
			}
		}

		cancel()
	}

	for _, sig := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
		if sig != 0 {
			_ = killGroup(r.cmd, sig) //nolint:errcheck // a group gone already is stopped
		}

		select {
		case <-r.exited:
			_ = killGroup(r.cmd, syscall.SIGKILL) //nolint:errcheck // nothing may be left to kill
			r.log.Close()

			return r.err
		case <-time.After(stopWait(sig, len(r.conf.Stop) > 0)):
		}
	}

	r.log.Close()

	return errors.Join(r.err, fmt.Errorf("%w: the set-up did not stop", ErrCommand))
}

// stopWait is how long a stop waits at each stage: briefly for a set-up
// that a stop command already asked to go, then ten seconds for TERM.
func stopWait(sig syscall.Signal, asked bool) time.Duration {
	if sig == 0 && !asked {
		return 0
	}

	return 10 * time.Second
}
