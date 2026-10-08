package testenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// cp is where cp is, found when the test binary starts: a test may set
// PATH to nothing of the machine's before it writes a script.
var cp = func() string { //nolint:gochecknoglobals // found once, before any test changes PATH
	if p, err := exec.LookPath("cp"); err == nil {
		return p
	}

	return "/bin/cp"
}()

// WriteExecutable writes a script a test runs, and makes it executable,
// without this process ever holding the file open for writing: a child
// forked meanwhile would hold a copy of the write descriptor until it execs,
// and running the script then fails with "text file busy". So a process of
// its own copies the content into place.
func WriteExecutable(ctx context.Context, path string, content []byte) error {
	src, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".src-*")
	if err != nil {
		return fmt.Errorf("testenv: %w", err)
	}

	defer func() {
		if err := os.Remove(src.Name()); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		}
	}()

	if _, err := src.Write(content); err != nil {
		return fmt.Errorf("testenv: %w", errors.Join(err, src.Close()))
	}

	if err := src.Close(); err != nil {
		return fmt.Errorf("testenv: %w", err)
	}

	//nolint:gosec // cp of the test's own files
	if out, err := exec.CommandContext(ctx, cp, src.Name(), path).CombinedOutput(); err != nil {
		return fmt.Errorf("testenv: cp: %w: %s", err, out)
	}

	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // a script must be executable to run
		return fmt.Errorf("testenv: %w", err)
	}

	return nil
}
