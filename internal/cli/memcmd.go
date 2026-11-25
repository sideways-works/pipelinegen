package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/membroker"
	"github.com/sideways-works/pipelinegen/internal/memory"
)

// The broker's defaults: where it listens, and how long a grant counts against the worker's memory.
const (
	brokerListen = ":7480"
	brokerRamp   = 5 * time.Minute
)

func runMemory(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"serve", "hand out a worker's memory to the batches that start on it, in the order they ask: [--listen :7480] [--ramp 5m]", func(ctx context.Context, args []string) error {
			return memoryServe(ctx, args, stdout)
		}},
		{"sample", "measure the peak memory of a sample of a suite's slices on this machine and record it as their peaks: --suite <suite> [--slices <n>]", func(ctx context.Context, args []string) error {
			return memorySampleRun(ctx, args, stdout)
		}},
	})
}

// memoryServe runs the broker of the worker it runs on: beside the Concourse worker, in its container,
// so the memory it reads is what the worker's tasks may use.
func memoryServe(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		listen, root, cgroup string
		ramp                 time.Duration
	)

	fs := flag.NewFlagSet("memory serve", flag.ContinueOnError)
	textVar(fs, &listen, "listen", brokerListen, "the address the broker listens on")
	fs.DurationVar(&ramp, "ramp", brokerRamp, "how long a grant counts against the worker's free memory, by when the batch's own use shows in it")
	textVar(fs, &root, "root", "/", "where the machine's /proc and /sys are")
	textVar(fs, &cgroup, "cgroup", "", "the cgroup v2 path whose limit holds the worker's tasks, for a broker outside it: the first line of /proc/<the worker's pid>/cgroup less its 0::, such as /system.slice/docker-<id>.scope (default the broker's own)")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("memory serve: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	b := &membroker.Broker{
		Read: func() (memory.Reading, float64, error) {
			read := memory.Read
			if cgroup != "" {
				read = func(root string) (memory.Reading, error) { return memory.ReadAt(root, cgroup) }
			}

			r, err := read(root)

			return r, memory.Pressure(root), err
		},
		Ramp: ramp,
		Log:  stdout,
	}

	if err := say(stdout, "memory broker on %s, grants counting %s\n", l.Addr(), ramp); err != nil {
		return err
	}

	return b.Serve(ctx, l)
}
