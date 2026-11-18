package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mainBranch is the branch a patch release is cut from.
const mainBranch = "main"

// Patch releases the head of main of the clone in dir as tag, the steps a maintainer takes by hand
// for a patch release: main must be origin's main and the clone clean. The tag goes on main's head,
// with the tag of each nested module released with it, and both are pushed, unless the tag is there
// already. The build goes into out, unless out holds one from an earlier try; then it is published
// to repo (owner/name), downloaded again and checked against the build's SHA256SUMS.
func Patch(ctx context.Context, o IO, dir, repo, tag, out string) error {
	if !versionTag.MatchString(tag) {
		return refuse("%s is no version tag (v1.2.3, or v1.2.3-rc.1 for a candidate)", tag)
	}

	dir, err := filepath.Abs(dir)
	if err == nil {
		out, err = filepath.Abs(out)
	}

	if err != nil {
		return fmt.Errorf("patch: %w", err)
	}

	if err := atOriginMain(ctx, o, dir); err != nil {
		return err
	}

	if err := tagHead(ctx, o, dir, tag); err != nil {
		return err
	}

	if exists(filepath.Join(out, sumsFile)) {
		_, err = fmt.Fprintf(o.Stderr, "keeping the build in %s\n", out)
	} else {
		err = Build(ctx, o, dir, tag, out)
	}

	if err != nil {
		return err
	}

	if err := Publish(ctx, o, repo, tag, out); err != nil {
		return err
	}

	sums, err := verifyDownload(ctx, o, repo, tag, out)
	if err != nil {
		return err
	}

	head, err := output(ctx, dir, "git", "-C", dir, "rev-parse", "--short", tag+"^{commit}")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(o.Stdout, "%s %s (%s) verified by download, SHA256SUMS %s\n", repo, tag, head, sums)

	return err //nolint:wrapcheck // the run's own output
}

func atOriginMain(ctx context.Context, o IO, dir string) error {
	if err := o.run(ctx, dir, os.Environ(), "git", "fetch", "-q", "origin"); err != nil {
		return err
	}

	checkout := []string{"checkout", "-q", mainBranch}
	if !quiet(ctx, dir, "git", "rev-parse", "-q", "--verify", "refs/heads/"+mainBranch) {
		checkout = []string{"checkout", "-q", "-b", mainBranch, "origin/" + mainBranch}
	}

	if err := o.run(ctx, dir, os.Environ(), "git", checkout...); err != nil {
		return err
	}

	if !quiet(ctx, dir, "git", "merge", "-q", "--ff-only", "origin/"+mainBranch) {
		return refuse("%s: main is not at origin/main and doesn't fast-forward to it", dir)
	}

	status, err := output(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return err
	}

	if status != "" {
		return refuse("%s has changes that aren't committed:\n%s", dir, status)
	}

	return nil
}

// tagHead tags main's head as tag, and each nested module released with it as SUB/tag, and pushes
// those tags alone; a tag that exists is left as it is, since a pushed tag never moves.
func tagHead(ctx context.Context, o IO, dir, tag string) error {
	if hasTag(ctx, dir, tag) {
		_, err := fmt.Fprintf(o.Stderr, "%s is tagged already\n", tag)

		return err //nolint:wrapcheck // the run's own output
	}

	subs, err := releasedWith(dir)
	if err != nil {
		return err
	}

	refs := []string{"refs/tags/" + tag}
	for _, sub := range subs {
		refs = append(refs, "refs/tags/"+sub+"/"+tag)
	}

	for _, ref := range refs {
		if err := o.run(ctx, dir, os.Environ(), "git", "tag", strings.TrimPrefix(ref, "refs/tags/")); err != nil {
			return err
		}
	}

	return o.run(ctx, dir, os.Environ(), "git", append([]string{"push", "-q", "origin"}, refs...)...)
}

// verifyDownload downloads the release into a directory of its own and checks it against its
// SHA256SUMS, which must be the build's; it gives that file's own checksum.
func verifyDownload(ctx context.Context, o IO, repo, tag, out string) (string, error) {
	got, err := os.MkdirTemp("", "release-download-")
	if err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}

	defer func() { _ = os.RemoveAll(got) }() //nolint:errcheck // a scratch directory

	if err := (IO{Stdout: o.Stderr, Stderr: o.Stderr}).run(ctx, "", os.Environ(), "gh", "release", "download", tag, "-R", repo, "-D", got); err != nil {
		return "", err
	}

	if err := checkSums(got); err != nil {
		return "", refuse("the files of %s's release %s don't match its SHA256SUMS", repo, tag)
	}

	published, errA := os.ReadFile(filepath.Join(got, sumsFile)) //nolint:gosec // the release's own sums
	built, errB := os.ReadFile(filepath.Join(out, sumsFile))     //nolint:gosec // the build's own sums

	if errA != nil || errB != nil || !bytes.Equal(published, built) {
		return "", refuse("%s's release %s holds other files than the build in %s", repo, tag, out)
	}

	sum := sha256.Sum256(built)

	return hex.EncodeToString(sum[:]), nil
}
