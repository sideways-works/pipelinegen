package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// mainBranch is the branch a patch release is cut from.
const mainBranch = "main"

// Patch releases the head of main of the clone in dir as tag, the steps a maintainer takes by hand
// for a patch release: main must be origin's main and the clone clean. The tag goes on main's head,
// with the tag of each nested module released with it, and both are pushed, unless the tag is there
// already. The build goes into out, unless out holds one from an earlier try; then it is published
// to repo (owner/name), downloaded again and checked against the build's SHA256SUMS. A module in a
// directory of the clone (dir) is tagged DIR/vX.Y.Z, its nested modules under it.
func Patch(ctx context.Context, o IO, dir, repo, tag, out string) error {
	m, err := locate(ctx, dir, tag)
	if err != nil {
		return err
	}

	out, err = filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("patch: %w", err)
	}

	dir, tag = m.repo, m.tag

	if err := atOriginMain(ctx, o, dir); err != nil {
		return err
	}

	names, created, err := tagLocally(ctx, o, m)
	if err != nil {
		return err
	}

	// The tags reach origin only once the build passed its checks: a pushed tag never moves, so
	// one pushed for a build that then failed could never be released.
	if exists(filepath.Join(out, sumsFile)) {
		_, err = fmt.Fprintf(o.Stderr, "keeping the build in %s\n", out)
	} else {
		err = Build(ctx, o, m.dir(dir), tag, out)
	}

	if err != nil {
		return errors.Join(err, untag(ctx, o, dir, created))
	}

	if err := pushTags(ctx, o, dir, names); err != nil {
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

	status, err := output(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return err
	}

	if status != "" {
		return refuse("%s has changes that aren't committed:\n%s", dir, status)
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

	return nil
}

// tagLocally tags main's head with the module's tag, and each nested module released with it with
// its own, unless the tag exists (a pushed tag never moves). It gives every tag of the release and
// those it created.
func tagLocally(ctx context.Context, o IO, m target) (names, created []string, err error) {
	subs, err := releasedWith(m.dir(m.repo))
	if err != nil {
		return nil, nil, err
	}

	dir, tag := m.repo, m.tag

	names = []string{tag}
	for _, sub := range subs {
		names = append(names, m.nestedTag(sub))
	}

	if hasTag(ctx, dir, tag) {
		if _, err := fmt.Fprintf(o.Stderr, "%s is tagged already\n", tag); err != nil {
			return nil, nil, err //nolint:wrapcheck // the run's own output
		}

		return names, nil, nil
	}

	for _, name := range names {
		if err := o.run(ctx, dir, os.Environ(), "git", "tag", name); err != nil {
			return nil, created, errors.Join(err, untag(ctx, o, dir, created))
		}

		created = append(created, name)
	}

	return names, created, nil
}

// pushTags pushes the release's tags: again on every run, so a push that failed before is made up,
// and a remote tag of the same name elsewhere fails it.
func pushTags(ctx context.Context, o IO, dir string, names []string) error {
	refs := []string{"push", "-q", "origin"}

	for _, name := range names {
		if hasTag(ctx, dir, name) {
			refs = append(refs, "refs/tags/"+name)
		}
	}

	return o.run(ctx, dir, os.Environ(), "git", refs...)
}

// untag deletes the local tags a run created, for a build that failed before they were pushed.
func untag(ctx context.Context, o IO, dir string, names []string) error {
	if len(names) == 0 {
		return nil
	}

	return o.run(ctx, dir, os.Environ(), "git", append([]string{"tag", "-d"}, names...)...)
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

// Check builds the tag of the module in dir afresh and fails unless repo's published release of it
// holds exactly those files, byte for byte: a released file holds the code of its tag.
func Check(ctx context.Context, o IO, dir, repo, tag string) error {
	m, err := locate(ctx, dir, tag)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "release-check-")
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	defer func() { _ = os.RemoveAll(work) }() //nolint:errcheck // a scratch directory

	out := filepath.Join(work, "out")
	if err := Build(ctx, IO{Stdout: discard{}, Stderr: o.Stderr}, dir, m.tag, out); err != nil {
		return err
	}

	sums, err := verifyDownload(ctx, o, repo, m.tag, out)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(o.Stdout, "%s %s is byte for byte a fresh build of its tag, SHA256SUMS %s\n", repo, m.tag, sums)

	return err //nolint:wrapcheck // the run's own output
}
