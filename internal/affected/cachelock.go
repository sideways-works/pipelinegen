package affected

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockCache holds the cache for one planning at a time: a second plan
// writing the shared work tree while the first hashes would leave a snapshot
// of a half-written tree, cached for every later plan. The lock goes with
// the process, so one that dies frees it.
func lockCache(cache string) (func(), error) {
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(cache, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // pipelinegen's own cache
	if err != nil {
		return nil, fmt.Errorf("cache lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close() //nolint:errcheck // failing anyway

		return nil, fmt.Errorf("cache lock: %w", err)
	}

	return func() { f.Close() }, nil //nolint:errcheck // closing frees the lock
}
