package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// bookkeepingDir is where a suite job's start leaves what its batches' ends read of the tree under test.
const bookkeepingDir = "bookkeeping"

// sliceBookkeepingFiles writes what a batch's end reads of the tree under test, so the end can run where
// the tree is not: the config. The end reads the rest from the run's records, the batch's outputs and the
// trigger, and the slices' observations from what the batch kept; only a slice the batch left unobserved
// needs the tree.
func sliceBookkeepingFiles(ctx context.Context, args []string, stdout io.Writer) error {
	var root, out string

	fs := flag.NewFlagSet("slice bookkeeping-files", flag.ContinueOnError)
	pathVar(fs, &root, "root", treeDir, "the tree under test", reads)
	pathVar(fs, &out, "out", bookkeepingDir, "where the files a batch's end reads go", writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	data, err := os.ReadFile(filepath.Join(root, config.FileName)) //nolint:gosec // the tree under test's config
	if err != nil {
		return fmt.Errorf("bookkeeping files: %w", err)
	}

	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("bookkeeping files: %w", err)
	}

	if err := os.WriteFile(filepath.Join(out, config.FileName), data, 0o644); err != nil { //nolint:gosec // the config, which every task may read
		return fmt.Errorf("bookkeeping files: %w", err)
	}

	return say(stdout, "bookkeeping files: %s (%d bytes)\n", config.FileName, len(data))
}
