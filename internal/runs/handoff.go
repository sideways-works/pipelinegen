package runs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ErrSuperseded is a change whose branch moved on since it was listed: the newer head is the sweep's
// to plan, never this change's under its older id.
var ErrSuperseded = errors.New("superseded, not run")

var errBadBranch = errors.New("not a branch name")

// IsHandOff tells whether a trigger is a forge's hand-off: a run.json
// and no checkout.
func IsHandOff(dir string) bool { return triggerForm(dir) == formHandOff }

// CheckoutChange makes empty dir a checkout of a forge's change from the trusted sweep's remote,
// its tip required to be the change's head; the credential reaches git through the environment alone.
func CheckoutChange(ctx context.Context, dir string, ch Change, auth Auth) error {
	if err := checkBranch(ctx, ch.Branch); err != nil {
		return err
	}

	if u, err := url.Parse(ch.Remote); err == nil && u.Scheme != "" && u.Scheme != "ssh" {
		if _, password := u.User.Password(); u.User != nil && (password || u.Scheme == "https" || u.Scheme == "http") {
			return fmt.Errorf("%w: the change's remote carries a user", ErrTrigger)
		}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrTrigger, err)
	}

	tracking := "refs/remotes/origin/" + ch.Branch

	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", ch.Remote},
	} {
		if _, err := git(ctx, dir, args...); err != nil {
			return err
		}
	}

	if _, err := gitEnv(ctx, dir, tokenEnv(auth), "fetch", "-q", "--no-tags", "origin", "+refs/heads/"+ch.Branch+":"+tracking); err != nil {
		return err
	}

	tip, err := git(ctx, dir, "rev-parse", "--verify", "--end-of-options", tracking+"^{commit}")
	if err != nil {
		return err
	}

	if strings.TrimSpace(tip) != ch.Commit {
		return fmt.Errorf("%w: %s is at %s now, not %s", ErrSuperseded, ch.Branch, short(strings.TrimSpace(tip)), short(ch.Commit))
	}

	_, err = git(ctx, dir, "checkout", "-q", "--detach", ch.Commit)

	return err
}

// checkBranch refuses a branch name git wouldn't take, or one that reads as
// an option.
func checkBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\n\r\x00") {
		return fmt.Errorf("%w: %q", errBadBranch, branch)
	}

	if _, err := git(ctx, "", "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("%w: %q", errBadBranch, branch)
	}

	return nil
}
