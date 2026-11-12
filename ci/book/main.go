// Command book runs every step of the book's guide on a fresh copy of
// example-notes and fails when what the tool printed differs from
// docs/transcripts, the outputs the book shows. With --update it writes
// the new outputs into docs/transcripts and the pages instead.
//
//	go run ./ci/book [--update] <scratch dir>
//
// The outputs are the pinned releases': the pipelinegen on PATH must be
// the version example-notes pins, and each plugin in $PLUGINS its pinned
// binary. The steps need pipelinegen, bazel (bazelisk), go, node and a
// Playwright browser on PATH.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sideways-works/pipelinegen/internal/book"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	o := book.Options{Plugins: os.Getenv("PLUGINS"), Stdout: os.Stdout, Stderr: os.Stderr}

	if len(args) > 0 && args[0] == "--update" {
		o.Update = true
		args = args[1:]
	}

	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: go run ./ci/book [--update] <scratch dir>")

		return 2
	}

	if o.Plugins == "" {
		fmt.Fprintln(os.Stderr, "PLUGINS: a directory holding the pinned plugins: go-sbom, go-correlator, openapi-sbom, oapi-codegen-correlator, concourse, go, playwright")

		return 2
	}

	root, err := checkout()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	if err := os.MkdirAll(args[0], 0o750); err != nil { //nolint:gosec // the scratch directory the person names
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	scratch, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	o.Root, o.Scratch = root, scratch

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := book.Check(ctx, o); err != nil {
		if !errors.Is(err, book.ErrDiffers) {
			fmt.Fprintln(os.Stderr, "book:", err)
		}

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
		if _, err := os.Stat(filepath.Join(dir, "example-notes", "pipelinegen.yaml")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run it in pipelinegen's checkout: no example-notes above here") //nolint:err113 // said once, to the person
		}

		dir = parent
	}
}
