package affected

import (
	"context"
	"path"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
)

// ownedPaths lists the files Bazel knows in the workspace, relative to the
// repository's root: the source files its targets own, and the BUILD and
// .bzl files that define them.
func ownedPaths(ctx context.Context, bz *bazel.Client, workspace string) (map[string]bool, error) {
	out, err := bz.Run(ctx, "query", `kind("source file", deps(//...)) + buildfiles(//...)`, "--output=label", "--keep_going=false")
	if err != nil {
		return nil, err
	}

	owned := map[string]bool{}

	for line := range strings.SplitSeq(string(out), "\n") {
		p, ok := bazel.LabelPath(strings.TrimSpace(line))
		if !ok {
			continue
		}

		owned[path.Join(workspace, p)] = true
	}

	return owned, nil
}
