package plugins

import "github.com/sideways-works/pipelinegen/internal/testenv"

// The tests' git sees none of the machine's config.
func init() { testenv.Git() } //nolint:gochecknoinits // before any test runs git

// The tests' caches read back every set they store: one that can't be
// found from each of its inputs fails the write.
func init() { checkStored = true } //nolint:gochecknoinits // before any test stores a set
