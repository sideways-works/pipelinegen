// Package release builds, twice and byte for byte alike, and publishes a release of pipelinegen or a plugin,
// only from a commit a run of every test passed on; a published release is never changed.
package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrRelease is a release the build or publish refuses; its message says
// why.
var ErrRelease = errors.New("release")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRelease, fmt.Sprintf(format, args...))
}

var versionTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

var candidate = regexp.MustCompile(`-rc\.[0-9]+$`)

type IO struct {
	Stdout, Stderr io.Writer
}

func (o IO) run(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the release's own tools
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = o.Stdout, o.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

func output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the release's own tools
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimRight(string(out), "\n"), nil
}

func quiet(ctx context.Context, dir string, name string, args ...string) bool {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the release's own tools
	cmd.Dir = dir

	return cmd.Run() == nil
}

func hasTag(ctx context.Context, dir, tag string) bool {
	return quiet(ctx, dir, "git", "-C", dir, "rev-parse", "-q", "--verify", "refs/tags/"+tag+"^{commit}")
}

// legal fails unless the tagged commit holds the LICENSE and NOTICE the release carries: the
// module's own, or the repository's.
func legal(ctx context.Context, m target) error {
	for _, f := range []string{licenseFile, noticeFileName} {
		at := func(p string) bool {
			return quiet(ctx, m.repo, "git", "-C", m.repo, "cat-file", "-e", "refs/tags/"+m.tag+"^{commit}:"+p)
		}

		if !at(m.path(f)) && !at(f) {
			return refuse("%s has no %s", m.tag, f)
		}
	}

	return nil
}

// changelogEntry fails unless the module's CHANGELOG.md, when it has one, has the release's entry.
func changelogEntry(ctx context.Context, m target) error {
	dir, tag := m.repo, m.tag

	version := strings.TrimPrefix(m.version, "v")
	if i := strings.Index(version, "-rc."); i >= 0 {
		version = version[:i]
	}

	ref := "refs/tags/" + tag + "^{commit}:" + m.path("CHANGELOG.md")
	if !quiet(ctx, dir, "git", "-C", dir, "cat-file", "-e", ref) {
		return nil
	}

	log, err := output(ctx, dir, "git", "-C", dir, "show", ref)
	if err != nil {
		return err
	}

	if regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\]`).MatchString(log) {
		return nil
	}

	if candidate.MatchString(m.version) && regexp.MustCompile(`(?m)^## \[Unreleased\]`).MatchString(log) {
		return nil
	}

	also := ""
	if strings.Contains(m.version, "-rc") {
		also = ` or "## [Unreleased]"`
	}

	return refuse(`CHANGELOG.md at %s has no entry "## [%s]"%s`, tag, version, also)
}

// Build builds the tag of the module in dir, the root of a checkout or a module directory in one,
// into out, twice, and prints SHA256SUMS once both builds gave the same files. A module directory's
// tag is DIR/vX.Y.Z, given whole or as its version.
func Build(ctx context.Context, o IO, dir, tag, out string) error {
	// absolute, since the builds run in other directories
	m, err := locate(ctx, dir, tag)
	if err != nil {
		return err
	}

	out, err = filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	switch {
	case exists(out):
		return refuse("%s exists", out)
	case !hasTag(ctx, m.repo, m.tag):
		return refuse("%s has no tag %s", m.repo, m.tag)
	}

	if err := legal(ctx, m); err != nil {
		return err
	}

	if err := changelogEntry(ctx, m); err != nil {
		return err
	}

	tag = m.tag

	check, err := os.MkdirTemp("", "release-check-")
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	defer func() { _ = os.RemoveAll(check) }() //nolint:errcheck // a scratch directory

	if err := buildOnce(ctx, o, m, out); err != nil {
		return err
	}

	again := filepath.Join(check, "again")
	if err := buildOnce(ctx, o, m, again); err != nil {
		return err
	}

	first, err := os.ReadFile(filepath.Join(out, sumsFile)) //nolint:gosec // the build's own sums
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	second, err := os.ReadFile(filepath.Join(again, sumsFile)) //nolint:gosec // the build's own sums
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	if !bytes.Equal(first, second) {
		return refuse("two builds of %s differ: %s", tag, diffLine(ctx, filepath.Join(out, sumsFile), filepath.Join(again, sumsFile)))
	}

	if _, err := o.Stdout.Write(first); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	return nil
}

func diffLine(ctx context.Context, a, b string) string {
	out, _ := exec.CommandContext(ctx, "diff", a, b).Output() //nolint:errcheck,gosec // diff exits 1 when the files differ, which they do

	return strings.ReplaceAll(string(out), "\n", " ")
}

func exists(p string) bool {
	_, err := os.Lstat(p)

	return err == nil
}

// Tested fails unless one of the named commit statuses passed on the
// commit.
func Tested(ctx context.Context, o IO, repo, commit string, contexts []string) error {
	states, err := output(ctx, "", "gh", "api", "repos/"+repo+"/commits/"+commit+"/status?per_page=100", "--jq", `.statuses[] | "\(.context) \(.state)"`)
	if err != nil {
		return refuse("can't read the statuses of %s", commit)
	}

	lines := strings.Split(states, "\n")

	for _, c := range contexts {
		if slices.Contains(lines, c+" success") {
			_, err := fmt.Fprintf(o.Stdout, "%s passed %s\n", commit, c)

			return err //nolint:wrapcheck // the run's own output
		}
	}

	return refuse("no run of every test passed on %s (%s): run one before releasing it", commit, strings.Join(contexts, " "))
}

// Publish attaches out's files to the tag's GitHub release of repo (owner/name); an existing release is never
// changed: publishing succeeds when it holds exactly this build and fails when it doesn't.
func Publish(ctx context.Context, o IO, repo, tag, out string) error {
	out, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	if !exists(filepath.Join(out, sumsFile)) {
		return refuse("%s holds no build", out)
	}

	if err := checkSums(out); err != nil {
		return refuse("%s's files don't match their SHA256SUMS", out)
	}

	if quiet(ctx, "", "gh", "release", "view", tag, "-R", repo) {
		// configs pin a release by its checksums: never change a published
		// one
		published, err := os.MkdirTemp("", "release-published-")
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}

		defer func() { _ = os.RemoveAll(published) }() //nolint:errcheck // a scratch directory

		if err := (IO{Stdout: o.Stderr, Stderr: o.Stderr}).run(ctx, "", os.Environ(), "gh", "release", "download", tag, "-R", repo, "-p", sumsFile, "-D", published); err != nil {
			return refuse("%s is released without SHA256SUMS; leaving it alone", tag)
		}

		a, errA := os.ReadFile(filepath.Join(published, sumsFile)) //nolint:gosec // the release's own sums
		b, errB := os.ReadFile(filepath.Join(out, sumsFile))       //nolint:gosec // the build's own sums

		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			return refuse("%s is released with other files than this build: %s", tag, diffLine(ctx, filepath.Join(published, sumsFile), filepath.Join(out, sumsFile)))
		}

		_, err = fmt.Fprintf(o.Stdout, "%s is released with exactly these files\n", tag)

		return err //nolint:wrapcheck // the run's own output
	}

	files, err := filepath.Glob(filepath.Join(out, "*"))
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	args := []string{"release", "create", tag, "-R", repo, "--verify-tag", "--title", tag}
	// a candidate is there to be tried, never what a newest-release lookup
	// finds
	if candidate.MatchString(tag) {
		args = append(args, "--prerelease", "--latest=false")
	}

	args = append(append(args, "--notes", "Check the files against SHA256SUMS before you run them."), files...)

	return o.run(ctx, "", os.Environ(), "gh", args...)
}
