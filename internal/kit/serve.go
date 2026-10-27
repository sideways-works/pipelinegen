package kit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/osproc"
)

const readyWithin = 2 * time.Minute

const stopWithin = 10 * time.Second

var errStack = errors.New("the stack")

type server struct {
	cmd     *exec.Cmd
	done    chan error
	exited  bool
	exitErr error
}

func startServer(ctx context.Context, batch string, r kitRecord) (*server, error) {
	if r.Ready == "" {
		return nil, fmt.Errorf("%w: a record entry with serve names its ready file (\"ready\": a glob under the batch's directory)", errStack)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), r.Serve[0], r.Serve[1:]...) //nolint:gosec // the fixture's own stack, run by its plugin's kit
	cmd.Dir = batch

	cmd.Env = append(os.Environ(), "PIPELINEGEN_RECORD=true")
	osproc.OwnGroup(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %w", errStack, err)
	}

	s := &server{cmd: cmd, done: make(chan error, 1)}

	go func() { s.done <- cmd.Wait() }()

	ready := func() bool {
		m, _ := filepath.Glob(filepath.Join(batch, r.Ready)) //nolint:errcheck // a bad pattern never matches

		return len(m) > 0
	}

	for deadline := time.Now().Add(readyWithin); ; {
		if ready() {
			return s, nil
		}

		select {
		case err := <-s.done:
			// ready and gone at once: the last collect finds it gone
			if s.exited, s.exitErr = true, err; ready() {
				return s, nil
			}

			s.kill()

			return nil, fmt.Errorf("%w exited before it was ready (%s): %w", errStack, r.Ready, err)
		case <-time.After(50 * time.Millisecond):
		}

		if time.Now().After(deadline) {
			s.kill()

			return nil, fmt.Errorf("%w wasn't ready within %s: no %s", errStack, readyWithin, r.Ready)
		}
	}
}

// stop ends the stack after the last collect: it must still run then, and
// leave no process of its group behind.
func (s *server) stop() []string {
	var problems []string

	if !s.exited {
		select {
		case err := <-s.done:
			s.exited, s.exitErr = true, err
		default:
		}
	}

	if s.exited {
		s.kill()

		return []string{fmt.Sprintf("the stack exited before the last collect: %v", s.exitErr)}
	}

	_ = syscall.Kill(s.cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck // it may have ended already

	select {
	case <-s.done:
	case <-time.After(stopWithin):
		problems = append(problems, fmt.Sprintf("the stack didn't stop within %s of SIGTERM", stopWithin))
	}

	// a process of its group still there is one it left behind
	if syscall.Kill(-s.cmd.Process.Pid, 0) == nil {
		problems = append(problems, "the stack left a process of its group running after it stopped")
	}

	s.kill()

	return problems
}

func (s *server) kill() {
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck // it may have ended already
}
