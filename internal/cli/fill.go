package cli

import (
	"context"
	"flag"
	"os"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func fillFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("no-fill", false, "don't fill the caches the plugins read (a Go module cache, say) before planning; plugins then can't tell what depends on what is missing")
}

type localPlan struct {
	cache, bazelDiff, plugins string
	noFill                    *bool
}

func (l *localPlan) register(fs *flag.FlagSet) {
	pathVar(fs, &l.cache, "cache", defaultCache(), "where work trees and hashes are kept", reads|writes)
	pathVar(fs, &l.bazelDiff, "bazel-diff", "", "a bazel-diff binary to run instead of the pinned release", readsFile)
	pathVar(fs, &l.plugins, "plugins", "", "the directory holding the configured sbom and correlator plugins (default: tools/plugins under the root)", reads)
	l.noFill = fillFlag(fs)
}

// On a person's machine no planner prepare filled the plugins' caches; a pipeline's steps never get a filler.
func withFill(ctx context.Context, who string, off bool) context.Context {
	if off {
		return ctx
	}

	return plugins.WithFiller(ctx, &plugins.Filler{Who: who, Out: os.Stderr})
}
