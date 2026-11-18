// Command cmd checks this repository's commit messages: file <path> as the commit-msg hook, new
// for the commits a CI build brings, log <revs>... for any range.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/sideways-works/pipelinegen/internal/commitmsg"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: file <path> | new | log <revs>")

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}

	switch args[0] {
	case "file":
		if len(args) != 2 {
			return errUsage
		}

		msg, err := os.ReadFile(args[1]) //nolint:gosec // the message file git hands the hook
		if err != nil {
			return fmt.Errorf("read the message: %w", err)
		}

		return commitmsg.Check(string(msg))
	case "new":
		revs, err := commitmsg.New(ctx, ".")
		if err != nil {
			return err
		}

		return commitmsg.Commits(ctx, ".", revs...)
	case "log":
		return commitmsg.Commits(ctx, ".", args[1:]...)
	}

	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}
