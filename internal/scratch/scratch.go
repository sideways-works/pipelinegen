// Package scratch makes a step's work directories under its TMPDIR and removes them when it ends,
// fails or is stopped: a checkout of a proposed change must not outlive the step that made it.
package scratch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

var ErrScratch = errors.New("scratch directory")

type Dirs struct {
	mu   sync.Mutex
	dirs []string
	stop chan os.Signal
}

// Make makes a directory readable by this user only, under TMPDIR, named by prefix and a random
// part.
func (d *Dirs) Make(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix+"-")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrScratch, err)
	}

	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory its owner must enter, and only its owner
		_ = os.RemoveAll(dir) //nolint:errcheck // already failing

		return "", fmt.Errorf("%w: %w", ErrScratch, err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stop == nil {
		d.stop = make(chan os.Signal, 1)
		signal.Notify(d.stop, os.Interrupt, syscall.SIGTERM)

		go d.onSignal(d.stop)
	}

	d.dirs = append(d.dirs, dir)

	return dir, nil
}

func (d *Dirs) onSignal(stop chan os.Signal) {
	sig, ok := <-stop
	if !ok {
		return
	}

	d.mu.Lock()
	for _, dir := range d.dirs {
		_ = removeAll(dir) //nolint:errcheck // the process ends either way
	}
	d.mu.Unlock()

	signal.Reset(sig)

	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Signal(sig) //nolint:errcheck // the process ends either way
	}
}

func (d *Dirs) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	errs := make([]error, 0, len(d.dirs))

	for _, dir := range d.dirs {
		errs = append(errs, removeAll(dir))
	}

	d.dirs = nil

	if d.stop != nil {
		signal.Stop(d.stop)
		close(d.stop)
		d.stop = nil
	}

	return errors.Join(errs...)
}

func removeAll(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}

	_ = filepathWalkWritable(dir) //nolint:errcheck // the second removal says what stays

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("%w: %w", ErrScratch, err)
	}

	return nil
}

func filepathWalkWritable(dir string) error {
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:errcheck,gosec // to remove it next
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrScratch, err)
	}

	return nil
}
