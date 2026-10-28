// Command pipelinegen makes a CI pipeline run only the tests a change can
// break.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	"github.com/sideways-works/pipelinegen/internal/cli"
)

// envCPUProfile names a file the command writes a CPU profile of itself to.
const envCPUProfile = "PIPELINEGEN_CPU_PROFILE"

// Version is the release's tag, set by the release build.
var Version = "dev"

func main() {
	notDumpable()

	cli.Version = Version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	stopProfile := profile(os.Getenv(envCPUProfile))
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)

	stopProfile()
	stop()
	os.Exit(code)
}

func profile(path string) func() {
	if path == "" {
		return func() {}
	}

	f, err := os.Create(path) //nolint:gosec // the path the person running it named
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipelinegen: no CPU profile: %v\n", err)

		return func() {}
	}

	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintf(os.Stderr, "pipelinegen: no CPU profile: %v\n", err)

		_ = f.Close() //nolint:errcheck // nothing was written

		return func() {}
	}

	return func() {
		pprof.StopCPUProfile()

		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "pipelinegen: the CPU profile: %v\n", err)
		}
	}
}
