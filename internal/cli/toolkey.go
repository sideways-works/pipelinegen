package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
)

var (
	selfOnce sync.Once
	selfKey  string
)

// A snapshot made by other tools is never reused: its hashes may be made differently. The executable's
// content tells two development builds apart.
func toolKey(differ bazeldiff.Tool) string {
	selfOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			selfKey = "unknown: " + err.Error()

			return
		}

		selfKey = fileKey(exe)
	})

	return Version + " " + selfKey + " " + fileKey(differ.Path)
}

// fileKey is a file's content hash, or why there is none (which never
// equals a hash, so nothing cached is reused).
func fileKey(path string) string {
	f, err := os.Open(path) //nolint:gosec // the tools' own executables
	if err != nil {
		return "unreadable: " + err.Error()
	}
	defer f.Close() //nolint:errcheck // read only

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable: " + err.Error()
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}
