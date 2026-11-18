package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const seedJob = ".job-"

// orphanAge is how old a taken scratch repository must be before a later
// job removes it: one whose job died before putting it back.
const orphanAge = 24 * time.Hour

// claim takes the seed by renaming it to a name of the job's own, which moves all of it or nothing,
// so no two jobs hold one; a seed that fails its check is removed, and the job starts empty.
func (s *Store) claim(ctx context.Context) error {
	parent, base := filepath.Dir(s.Seed), filepath.Base(s.Seed)

	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	removeOrphans(parent, base)

	dir, err := os.MkdirTemp(parent, base+seedJob)
	if err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	s.Dir, s.temp, s.seeded = dir, true, true

	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	if os.Rename(s.Seed, dir) == nil && s.sound(ctx) {
		return nil
	}

	// none there, taken by another job, or broken: start empty
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	if err := os.Mkdir(dir, 0o750); err != nil {
		return fmt.Errorf("scratch repository: %w", err)
	}

	return nil
}

// sound tells whether every object the taken repository's refs reach is there and readable: a job
// killed while it was out, a full disk or a stray write leaves one that isn't.
func (s *Store) sound(ctx context.Context) bool {
	if _, err := os.Stat(filepath.Join(s.Dir, "HEAD")); err != nil {
		return false
	}

	_, err := s.git(ctx, nil, nil, "fsck", "--connectivity-only", "--no-progress", "--no-dangling")

	return err == nil
}

// putBack returns the scratch repository to the seed, packed when fetches left many packs; false when
// another job put one back first, and this one is then removed.
func (s *Store) putBack() bool {
	if out, err := s.offline(&strings.Builder{}, "count-objects", "-v"); err == nil && untidy(out) {
		if _, err := s.offline(&strings.Builder{}, "gc", "--quiet", "--prune=now"); err != nil {
			return false
		}
	}

	return os.Rename(s.Dir, s.Seed) == nil
}

// offline runs git on the scratch repository alone, with no remote and so
// no context: Close has none to give.
func (s *Store) offline(out *strings.Builder, args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec,noctx // git with the store's own arguments, on the scratch repository only
	cmd.Dir = s.Dir
	cmd.Env = s.env(nil)
	cmd.Stdout = out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: git %s: %w", ErrGit, strings.Join(args, " "), err)
	}

	return out.String(), nil
}

// untidy tells from count-objects -v whether the repository wants
// packing: many loose objects or many packs make every read slower.
func untidy(out string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}

		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}

		if (key == "count" && n > 2000) || (key == "packs" && n > 20) {
			return true
		}
	}

	return false
}

func removeOrphans(parent, base string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), base+seedJob) {
			continue
		}

		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > orphanAge {
			_ = os.RemoveAll(filepath.Join(parent, e.Name())) //nolint:errcheck // the next job tries again
		}
	}
}
