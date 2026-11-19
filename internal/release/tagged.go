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
// built into out and then published. A tag whose release exists is checked instead: its files must
// be this build's. A module in a directory of the checkout (source) releases its
// tag DIR/v1.2.3.
func Tagged(ctx context.Context, o IO, source, repo, out string, contexts []string) error {
	root, sub, err := place(ctx, source)
	if err != nil {
		return fmt.Errorf("tagged: %w", err)
	}

	tags, err := output(ctx, root, "git", "-C", root, "tag", "--points-at", "HEAD")
	if err != nil {
		return err
	}

	var tag string

	for t := range strings.SplitSeq(tags, "\n") {
		v, ok := t, sub == ""
		if !ok {
			v, ok = strings.CutPrefix(t, sub+"/")
		}

		if ok && releaseTag.MatchString(v) {
			tag = t

			break
		}
	}

	if tag == "" {
		return refuse("no version tag on the head of %s", source)
	}

	source = filepath.Join(root, filepath.FromSlash(sub))

	commit, err := output(ctx, source, "git", "-C", source, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	// A tag released already (by hand, say) is done when its published files are this build's,
	// byte for byte: they then hold the tag's code, whichever run tested it.
	if quiet(ctx, "", "gh", "release", "view", tag, "-R", repo) {
		if err := Build(ctx, o, source, tag, out); err != nil {
			return err
		}

		if _, err := verifyDownload(ctx, o, repo, tag, out); err != nil {
			return err
		}

		if _, err := fmt.Fprintf(o.Stdout, "%s is released already, and its files are this build's\n", tag); err != nil {
			return fmt.Errorf("tagged: %w", err)
		}

		return nil
	}

	if err := Tested(ctx, o, repo, commit, contexts); err != nil {
		return err
	}

	if err := Build(ctx, o, source, tag, out); err != nil {
		return err
	}

	return Publish(ctx, o, repo, tag, out)
}
