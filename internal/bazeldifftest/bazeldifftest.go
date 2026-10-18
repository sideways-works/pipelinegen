// Package bazeldifftest gives tests the real, pinned bazel-diff.
package bazeldifftest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// Tool returns the bazel-diff to test against: the binary
// PIPELINEGEN_TEST_BAZEL_DIFF names, or else the pinned release, fetched
// once into the user's cache and checked against its checksum. A test that
// can't have it fails; only -short mode, where nothing starts Bazel, skips.
func Tool(t *testing.T) bazeldiff.Tool {
	t.Helper()
	testrepo.NeedBazel(t)

	if path := os.Getenv("PIPELINEGEN_TEST_BAZEL_DIFF"); path != "" {
		return bazeldiff.Tool{Path: path}
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("no cache directory for bazel-diff: %v", err)
	}

	tool, err := bazeldiff.Fetch(t.Context(), &http.Client{Timeout: 5 * time.Minute}, filepath.Join(dir, "pipelinegen-test"), bazeldiff.Pinned)
	if err != nil {
		t.Fatalf("fetch the pinned bazel-diff (or set PIPELINEGEN_TEST_BAZEL_DIFF): %v", err)
	}

	return tool
}
