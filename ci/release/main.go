// Command release builds and publishes a release of pipelinegen or of one
// of its plugins, and writes a repository's NOTICE. Run it in pipelinegen's
// checkout:
//
//	go run ./ci/release build   DIR TAG OUT   build TAG of the repository checked out in DIR into OUT
//	go run ./ci/release publish REPO TAG OUT  attach OUT's files to TAG's GitHub release of REPO (owner/name)
//	go run ./ci/release tested  REPO COMMIT CONTEXT...
//	                                          fail unless one of the commit statuses named passed on COMMIT
//	go run ./ci/release notice  DIR           write the NOTICE the release of the repository in DIR needs
//
// What a build makes and refuses is in internal/release.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sideways-works/pipelinegen/internal/release"
)

const usage = "usage: go run ./ci/release build DIR TAG OUT | publish REPO TAG OUT | tested REPO COMMIT CONTEXT... | notice DIR"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:])

	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	o := release.IO{Stdout: os.Stdout, Stderr: os.Stderr}

	var err error

	switch {
	case len(args) == 4 && args[0] == "build":
		err = release.Build(ctx, o, args[1], args[2], args[3])
	case len(args) == 4 && args[0] == "publish":
		err = release.Publish(ctx, o, args[1], args[2], args[3])
	case len(args) >= 4 && args[0] == "tested":
		err = release.Tested(ctx, o, args[1], args[2], args[3:])
	case len(args) == 2 && args[0] == "notice":
		err = notice(ctx, o, args[1])
	default:
		fmt.Fprintln(os.Stderr, "release: "+usage)

		return 1
	}

	if err != nil {
		if errors.Is(err, release.ErrRelease) {
			fmt.Fprintln(os.Stderr, err)
		} else {
			fmt.Fprintln(os.Stderr, "release:", err)
		}

		return 1
	}

	return 0
}

func notice(ctx context.Context, o release.IO, dir string) error {
	text, err := release.Notice(ctx, o, dir)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, "NOTICE")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil { //nolint:gosec // the repository's NOTICE
		return fmt.Errorf("notice: %w", err)
	}

	fmt.Fprintln(os.Stderr, "wrote", path)

	return nil
}
