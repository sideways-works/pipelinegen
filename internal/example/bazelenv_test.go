package example

import (
	"os"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// TestMain gives the Bazel servers the tests start an output root of the test binary's own, and
// removes it when they end.
func TestMain(m *testing.M) { os.Exit(testenv.Bazel(m)) }
