// Package commitmsg checks this repository's commit messages: one conventional subject line of
// at most 72 characters and nothing else, so the history reads as a list of what changed.
package commitmsg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

const MaxSubject = 72

var ErrMessage = errors.New("commit message")

var subject = regexp.MustCompile(`^(feat|fix|docs|test|build|ci|refactor|chore|perf)(\([a-z0-9][a-z0-9/._-]*\))?: \S`)

// Check checks a whole message as git hands it to a commit-msg hook: its comment lines (those
// starting with #) are not part of it.
func Check(msg string) error {
	var lines []string

	for line := range strings.SplitSeq(msg, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}

		lines = append(lines, strings.TrimRight(line, "\r"))
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) == 0 {
		return fmt.Errorf("%w: empty", ErrMessage)
	}

	if len(lines) > 1 {
		return fmt.Errorf("%w: one subject line only, no body and no trailers: %q", ErrMessage, lines[0])
	}

	return CheckSubject(lines[0])
}

func CheckSubject(s string) error {
	switch {
	case s != strings.TrimSpace(s):
		return fmt.Errorf("%w: space around the subject: %q", ErrMessage, s)
	case !subject.MatchString(s):
		return fmt.Errorf("%w: not type(scope): subject with a conventional type (feat, fix, docs, test, build, ci, refactor, chore, perf): %q", ErrMessage, s)
	case utf8.RuneCountInString(s) > MaxSubject:
		return fmt.Errorf("%w: %d characters, at most %d: %q", ErrMessage, utf8.RuneCountInString(s), MaxSubject, s)
	}

	return nil
}

// New lists the commits a build of dir's HEAD brings: a merge's second parent's own, else HEAD
// alone; a landed commit can't be rewritten, so it is never checked again.
func New(ctx context.Context, dir string) ([]string, error) {
	parents, err := git(ctx, dir, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return nil, err
	}

	if len(strings.Fields(parents)) == 3 {
		return []string{"HEAD^1..HEAD^2"}, nil
	}

	return []string{"-1", "HEAD"}, nil
}

// Commits checks every commit of revs in dir, merges left out, and returns one error naming each
// that breaks the rule.
func Commits(ctx context.Context, dir string, revs ...string) error {
	out, err := git(ctx, dir, append([]string{"log", "--no-merges", "--format=%h%x00%B%x00"}, revs...)...)
	if err != nil {
		return err
	}

	var bad []error

	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		sha := strings.TrimSpace(fields[i])
		if err := Check(fields[i+1]); err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", sha, err))
		}
	}

	return errors.Join(bad...)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with this package's own arguments
	cmd.Dir = dir
	cmd.Env = secret.HardenGit(os.Environ())

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return string(out), nil
}
