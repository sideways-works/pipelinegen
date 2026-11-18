package release

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Tagged releases the version tag (v1.2.3, never a candidate) on the head of the checkout in source
// to repo, as the pipeline's release job does: only when one of the contexts passed on that commit,
// built into out and then published.
func Tagged(ctx context.Context, o IO, source, repo, out string, contexts []string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("tagged: %w", err)
	}

	tags, err := output(ctx, source, "git", "-C", source, "tag", "--points-at", "HEAD")
	if err != nil {
		return err
	}

	var tag string

	for t := range strings.SplitSeq(tags, "\n") {
		if releaseTag.MatchString(t) {
			tag = t

			break
		}
	}

	if tag == "" {
		return refuse("no version tag on the head of %s", source)
	}

	commit, err := output(ctx, source, "git", "-C", source, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	if err := Tested(ctx, o, repo, commit, contexts); err != nil {
		return err
	}

	if err := Build(ctx, o, source, tag, out); err != nil {
		return err
	}

	return Publish(ctx, o, repo, tag, out)
}
