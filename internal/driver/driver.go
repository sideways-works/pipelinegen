// Package driver runs a suite's steps: it calls each step's driver with the
// slices routed to it, reads the results they stream, runs again apart what
// a bundle left without a result, and hands every slice's boundaries to the
// batch's journal.
package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrDriver is a driver that failed: it couldn't start, wrote what isn't
// the protocol, or exited non-zero.
var ErrDriver = errors.New("driver failed")

// Driver is one step's driver.
type Driver interface {
	// Name is the driver's name in the config.
	Name() string
	// Describe is the driver's describe answer.
	Describe(ctx context.Context) (plugin.Description, error)
	// Run runs the request's slices, calling event for each line of the
	// stream as it comes. It returns the last lines the driver wrote on
	// stderr, which say why when a slice got no end.
	Run(ctx context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error)
	// Setup starts the set-up and returns once it is ready.
	Setup(ctx context.Context, req plugin.DriverRequest) (Running, error)
	// Prepare runs a prepare step.
	Prepare(ctx context.Context, req plugin.DriverRequest) error
}

// Running is a set-up that runs until stopped.
type Running interface {
	// Env is what the set-up exports to later steps, by name.
	Env() map[string]string
	// Stop stops it: whatever it started, within bounds.
	Stop(ctx context.Context) error
}

// Exec is a driver plugin: an executable called with a verb, its request
// on stdin.
type Exec struct {
	Label string
	Path  string
	Env   []string
	Dir   string
	Log   io.Writer
	// StopWait bounds each stage of stopping a set-up: its own exit once
	// its stdin closed, then after TERM (default 10 seconds each).
	StopWait time.Duration
}

// Name is the driver's name.
func (e *Exec) Name() string { return e.Label }

const tailLines = 15

func (e *Exec) command(ctx context.Context, verb string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.Path, verb) //nolint:gosec // a driver the config pins by checksum
	cmd.Dir = e.Dir
	cmd.Env = e.Env
	// its own process group, so whatever it started stops with it
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	return cmd
}

func killGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}

	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal %s: %w", sig, err)
	}

	return nil
}

// Describe asks the driver what it is.
func (e *Exec) Describe(ctx context.Context) (plugin.Description, error) {
	var d plugin.Description

	cmd := e.command(ctx, plugin.CallDescribe)
	cmd.Stdin = strings.NewReader("{}")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return d, fmt.Errorf("%w: %s describe: %w: %s", ErrDriver, e.Label, err, lastLines(stderr.String(), 5))
	}

	if err := json.Unmarshal(out, &d); err != nil {
		return d, fmt.Errorf("%w: %s describe: unreadable answer: %w", ErrDriver, e.Label, err)
	}

	return d, nil
}

// Run runs a run request and streams its events.
func (e *Exec) Run(ctx context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s run: %w", ErrDriver, e.Label, err)
	}

	cmd := e.command(ctx, plugin.CallRun)
	cmd.Env = withMarks(e.Env, req)
	cmd.Stdin = bytes.NewReader(in)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("%w: %s run: %w", ErrDriver, e.Label, err)
	}

	log := newTailWriter(e.Log, e.Label+" run: ")
	cmd.Stderr = log

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%w: %s run: %w", ErrDriver, e.Label, err)
	}

	readEvents(stdout, event, log)

	err = cmd.Wait()

	log.Close()

	if err != nil {
		return log.Tail(), fmt.Errorf("%w: %s run: %w", ErrDriver, e.Label, err)
	}

	return log.Tail(), nil
}

// readEvents reads a run's stream: one JSON event per line. A line that
// isn't one goes to the log, as does an event kind this protocol doesn't
// know, which a later protocol may add.
func readEvents(r io.Reader, event func(plugin.DriverEvent), log io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev plugin.DriverEvent
		if err := json.Unmarshal(line, &ev); err != nil || ev.Slice == "" {
			sayf(log, "not an event: %s\n", line)

			continue
		}

		event(ev)
	}
}

// Prepare runs a prepare request.
func (e *Exec) Prepare(ctx context.Context, req plugin.DriverRequest) error {
	in, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%w: %s prepare: %w", ErrDriver, e.Label, err)
	}

	cmd := e.command(ctx, plugin.CallPrepare)
	cmd.Stdin = bytes.NewReader(in)

	log := newTailWriter(e.Log, e.Label+" prepare: ")
	cmd.Stdout, cmd.Stderr = log, log

	err = cmd.Run()

	log.Close()

	if err != nil {
		return fmt.Errorf("%w: %s prepare: %w: %s", ErrDriver, e.Label, err, log.Tail())
	}

	return nil
}

// Setup starts a set-up and waits for its ready line.
func (e *Exec) Setup(ctx context.Context, req plugin.DriverRequest) (Running, error) { //nolint:ireturn // the interface every set-up keeps
	in, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, err)
	}

	// the set-up outlives this call: only its stop ends it
	cmd := e.command(context.WithoutCancel(ctx), plugin.CallSetup)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, err)
	}

	log := newTailWriter(e.Log, e.Label+" setup: ")
	cmd.Stderr = log

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, err)
	}

	r := &setupProc{cmd: cmd, stdin: stdin, log: log, wait: e.StopWait, exited: make(chan error, 1)}
	if r.wait == 0 {
		r.wait = 10 * time.Second
	}

	if _, err := stdin.Write(append(in, '\n')); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, err), r.Stop(ctx))
	}

	ready := make(chan error, 1)

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

		if !sc.Scan() {
			ready <- fmt.Errorf("%w: %s setup ended before it was ready: %s", ErrDriver, e.Label, log.Tail())

			return
		}

		var a plugin.SetupAnswer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			ready <- fmt.Errorf("%w: %s setup: unreadable ready line: %w", ErrDriver, e.Label, err)

			return
		}

		r.env = a.Env

		ready <- nil

		// anything more on stdout is for people
		for sc.Scan() {
			sayf(log, "%s\n", sc.Bytes())
		}
	}()

	go func() { r.exited <- cmd.Wait() }()

	select {
	case err := <-ready:
		if err != nil {
			return nil, errors.Join(err, r.Stop(ctx))
		}

		return r, nil
	case <-ctx.Done():
		return nil, errors.Join(fmt.Errorf("%w: %s setup: %w", ErrDriver, e.Label, ctx.Err()), r.Stop(ctx))
	}
}

type setupProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	log    *tailWriter
	wait   time.Duration
	env    map[string]string
	exited chan error

	once sync.Once
	err  error
}

func (r *setupProc) Env() map[string]string { return r.env }

// Stop closes the set-up's stdin, which asks it to stop what it started
// and exit; one that doesn't within the wait gets TERM, then KILL, each
// to its whole process group.
func (r *setupProc) Stop(context.Context) error {
	r.once.Do(func() {
		_ = r.stdin.Close() //nolint:errcheck // a process gone already has nothing to read

		for _, sig := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
			if sig != 0 {
				if err := killGroup(r.cmd, sig); err != nil {
					r.err = err
				}
			}

			select {
			case err := <-r.exited:
				// whatever the set-up started in its group stops with it
				_ = killGroup(r.cmd, syscall.SIGKILL) //nolint:errcheck // nothing may be left to kill

				if sig != 0 {
					r.err = fmt.Errorf("%w: the set-up did not stop when asked; stopped by %s", ErrDriver, sig)
				} else if err != nil {
					r.err = fmt.Errorf("%w: the set-up's stop: %w: %s", ErrDriver, err, r.log.Tail())
				}

				r.log.Close()

				return
			case <-time.After(r.wait):
			}
		}
	})

	return r.err
}

func sayf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}
