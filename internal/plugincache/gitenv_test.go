package plugincache

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// The tests' git sees none of the machine's config.
func init() { testenv.Git() } //nolint:gochecknoinits // before any test runs git

// Every test of the cache's remote level runs on refs and on a branch.
func TestMain(m *testing.M) { storetest.Both(m) }
