// Command proof plans changes to a small fixture repository through the
// released Go plugins, after recording what its tests ran, and prints what
// each plan selected (docs/proof.md).
//
//	go run ./ci/proof [--newest | --source] <work dir>
//
// --newest plans with each piece's newest release instead of the pins;
// --source plans with the tool built from this checkout. It needs git, flock,
// ps, Go 1.27.1 and bazelisk. PIPELINEGEN, PLUGINS and BAZEL_DIFF name
// binaries to use instead of the pinned releases, each checked against its
// pin; GITHUB_TOKEN goes only in a request header. PROOF_WORKERS (default 3)
// workers plan side by side with two Bazel servers each, whose Java heap
// PROOF_BAZEL_HEAP (default 1g) bounds. Every plan is also made with the tool
// built to bind forward (the pipelinegen_forward build tag) and compared. The
// work directory must be empty or missing.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/sideways-works/pipelinegen/internal/proof"
)

const usage = "usage: go run ./ci/proof [--newest | --source] <work dir>"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	o := proof.Options{
		Tool: os.Getenv("PIPELINEGEN"), Plugins: os.Getenv("PLUGINS"), BazelDiff: os.Getenv("BAZEL_DIFF"),
		Heap: os.Getenv("PROOF_BAZEL_HEAP"), Token: os.Getenv("GITHUB_TOKEN"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}

	for len(args) > 0 {
		switch args[0] {
		case "--newest":
			o.Newest = true
		case "--source":
			o.Source = true
		default:
			goto done
		}

		args = args[1:]
	}

done:
	if len(args) != 1 || (o.Newest && o.Source) {
		fmt.Fprintln(os.Stderr, usage)

		return 2
	}

	o.Work = args[0]

	if w := os.Getenv("PROOF_WORKERS"); w != "" {
		n, err := strconv.Atoi(w)
		if err != nil || n < 1 {
			fmt.Fprintln(os.Stderr, "PROOF_WORKERS is no number of workers:", w)

			return 2
		}

		o.Workers = n
	}

	root, err := checkout()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	o.Root = root

	// a stopped run exits, and stops the servers it started on the way
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()

	err = proof.Run(ctx, o)

	switch {
	case ctx.Err() != nil:
		return 143
	case errors.Is(err, proof.ErrUsage):
		fmt.Fprintln(os.Stderr, err)

		return 2
	case errors.Is(err, proof.ErrDiffers):
		return 1
	case err != nil:
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	return 0
}

func checkout() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("checkout: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "ci", "proof", "pins")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run it in pipelinegen's checkout: no ci/proof/pins above here") //nolint:err113 // said once, to the person
		}

		dir = parent
	}
}
