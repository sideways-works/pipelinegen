package book

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotPinned is a run whose pipelinegen or plugins are not the releases example-notes pins:
// their outputs would not be the book's.
var ErrNotPinned = errors.New("not the pinned releases")

var ErrDiffers = errors.New("the book's outputs differ")

type Options struct {
	// Root is the tool's checkout, Scratch the directory the steps run in, and Plugins the
	// directory of the pinned plugins.
	Root, Scratch, Plugins string
	// Update writes the new outputs into docs/transcripts and the pages instead of comparing them.
	Update         bool
	Stdout, Stderr io.Writer
}

func readPlugin(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec,wrapcheck // a pinned plugin's file to check; a missing one is the problem it names
}

// Check runs every step of the book on a fresh copy of example-notes and compares what the tool
// printed with docs/transcripts, or with Update writes it into them and the pages.
func Check(ctx context.Context, o Options) error {
	config, err := os.ReadFile(filepath.Join(o.Root, "example-notes", "pipelinegen.yaml"))
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	// the outputs are the pinned releases': the pipelinegen on PATH must be the version
	// example-notes pins, and each plugin its pinned binary
	version, err := exec.CommandContext(ctx, "pipelinegen", "version").Output()
	if err != nil {
		return fmt.Errorf("check: pipelinegen version: %w", err)
	}

	if bad := PinProblems(string(config), o.Plugins, strings.TrimRight(string(version), "\n"), readPlugin); len(bad) > 0 {
		return fmt.Errorf("%w:\n  %s", ErrNotPinned, strings.Join(bad, "\n  "))
	}

	work, raw := filepath.Join(o.Scratch, "work"), filepath.Join(o.Scratch, "raw")

	if err := Steps(ctx, o.Root, work, raw, o.Plugins); err != nil {
		return err
	}

	transcripts := filepath.Join(o.Root, "docs", "transcripts")

	if o.Update {
		old, _ := filepath.Glob(filepath.Join(transcripts, "*.txt")) //nolint:errcheck // a valid pattern
		for _, f := range old {
			if err := os.Remove(f); err != nil {
				return fmt.Errorf("check: %w", err)
			}
		}

		if err := Trim(raw, transcripts, work); err != nil {
			return err
		}

		for _, fill := range []struct {
			dir   string
			pages []string
		}{{filepath.Join(o.Root, "docs"), nil}, {o.Root, []string{filepath.Join(o.Root, "README.md")}}} {
			filled, err := Fill(fill.dir, transcripts, fill.pages...)
			for _, p := range filled {
				_, _ = fmt.Fprintln(o.Stdout, "filled", p) //nolint:errcheck // the log is for people
			}

			if err != nil {
				return err
			}
		}

		return nil
	}

	fresh := filepath.Join(o.Scratch, "transcripts")
	if err := Trim(raw, fresh, work); err != nil {
		return err
	}

	diff := exec.CommandContext(ctx, "diff", "-ru", transcripts, fresh) //nolint:gosec // the checkout's transcripts and the fresh ones
	diff.Stdout, diff.Stderr = o.Stdout, o.Stderr

	if err := diff.Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrDiffers, err)
	}

	return nil
}
