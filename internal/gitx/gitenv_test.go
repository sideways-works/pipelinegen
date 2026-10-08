package gitx

import "github.com/sideways-works/pipelinegen/internal/testenv"

// The tests' git sees none of the machine's config.
func init() { testenv.Git() } //nolint:gochecknoinits // before any test runs git
