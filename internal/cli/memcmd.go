package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/membroker"
	"github.com/sideways-works/pipelinegen/internal/memory"
)

// The broker's defaults: where it listens, how long a grant counts against the worker's memory, and how
// long the first ask in line holds back the others, so a proposed change's batch asking for most of the
// worker delays them at most that long.
const (
	brokerListen   = ":7480"
	brokerRamp     = 5 * time.Minute
	brokerFirstFor = 10 * time.Minute
	// one grant counts at most this, in MB: above the heaviest batch a worker runs, below half a worker.
	brokerMaxGrant = 16384
	// the cores the grants may hold, per core of the host: a batch's slices wait on more than their
	// cores alone, on disk and network, so a little more than the host's cores keeps them busy.
	brokerCPUFactor = 1.25
	// the CPU pressure, in percent, at or above which no ask that waits for CPU is granted.
	brokerCPUPressure = 40
	// how often the broker probes the tasks holding cores: a batch that waited before its setup never
	// releases its own.
	brokerProbeEvery = 30 * time.Second
)

func runMemory(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"serve", "hand out a worker's memory and cores to the batches that start on it, in the order they ask: [--listen :7480] [--ramp 5m]", func(ctx context.Context, args []string) error {
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
		listen, root, cgroup  string
		ramp, firstFor, probe time.Duration
		maxGrant              int64
		cpuFactor, cpuHold    float64
	)

	fs := flag.NewFlagSet("memory serve", flag.ContinueOnError)
	textVar(fs, &listen, "listen", brokerListen, "the address the broker listens on")
	fs.DurationVar(&ramp, "ramp", brokerRamp, "how long a grant counts against the worker's free memory, by when the batch's own use shows in it")
	fs.DurationVar(&firstFor, "first-for", brokerFirstFor, "how long the first ask in line holds back later ones the memory covers")
	fs.Int64Var(&maxGrant, "max-grant", brokerMaxGrant, "the most one grant counts against the free memory, in MB; 0 is no cap")
	fs.Float64Var(&cpuFactor, "cpu-factor", brokerCPUFactor, "the cores the grants of batches that wait for CPU may hold at once, per core of this machine; 0 counts no cores")
	fs.Float64Var(&cpuHold, "cpu-pressure", brokerCPUPressure, "the CPU pressure (PSI some avg10, percent) at or above which no batch that waits for CPU is granted; 0 holds none back")
	fs.DurationVar(&probe, "probe-every", brokerProbeEvery, "how often the broker asks whether the tasks holding cores are still there, ending the cores of one gone twice in a row; 0 asks never")
	textVar(fs, &root, "root", "/", "where the machine's /proc and /sys are")
	textVar(fs, &cgroup, "cgroup", "", "the cgroup v2 path whose limit holds the worker's tasks, for a broker outside it: the first line of /proc/<the worker's pid>/cgroup less its 0::, such as /system.slice/docker-<id>.scope (default the broker's own)")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if cpuFactor < 0 || cpuHold < 0 || cpuHold > 100 || probe < 0 {
		return fmt.Errorf("%w: --cpu-factor and --probe-every are at least 0, and --cpu-pressure a percentage", errUsage)
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
		Ramp:     ramp,
		FirstFor: firstFor,
		MaxGrant: maxGrant,
		Log:      stdout,
		// the broker runs on the worker's host, whose cores its tasks share
		CPUs:    float64(runtime.NumCPU()) * cpuFactor,
		CPUHold: cpuHold,
		ReadCPU: func() float64 { return memory.CPUPressure(root) },
	}

	if probe > 0 {
		b.Gone, b.ProbeEvery = membroker.TaskGone, probe
	}

	if err := say(stdout, "memory broker on %s, grants counting %s, %.1f cores\n", l.Addr(), ramp, b.CPUs); err != nil {
		return err
	}

	return b.Serve(ctx, l)
}
