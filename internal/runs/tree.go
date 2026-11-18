package runs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// ErrNoMerge is a pull request that doesn't merge into its target cleanly.
var ErrNoMerge = errors.New("does not merge cleanly")

// treeRef holds the merge commit in the trigger's repository, so that a
// clone of it carries the commit along.
const treeRef = "refs/pipelinegen/tree"

// Merge makes the commit a pull request is tested as, head merged into the target's tip, with fixed
// parents, tree, message, author and dates, so the planning job and every suite job get one id.
func Merge(ctx context.Context, repo, head, into string) (string, error) {
	tree, err := git(ctx, repo, "merge-tree", "--write-tree", into, head)
	if err != nil {
		return "", fmt.Errorf("%s into %s: %w", short(head), short(into), ErrNoMerge)
	}

	date, err := git(ctx, repo, "show", "-s", "--format=%cI", head)
	if err != nil {
		return "", err
	}

	env := []string{
		"GIT_AUTHOR_NAME=pipelinegen", "GIT_AUTHOR_EMAIL=pipelinegen@invalid", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=pipelinegen", "GIT_COMMITTER_EMAIL=pipelinegen@invalid", "GIT_COMMITTER_DATE=" + date,
	}

	merge, err := gitEnv(ctx, repo, env, "commit-tree", firstLine(tree), "-p", into, "-p", head,
		"-m", "Merge "+head+" into "+into)
	if err != nil {
		return "", err
	}

	if _, err := git(ctx, repo, "update-ref", treeRef, merge); err != nil {
		return "", err
	}

	return merge, nil
}

// Auth is how git authenticates to a remote over HTTPS: Token, sent only
// to URLs that start with Scope; nothing without either.
type Auth struct {
	Scope string
	Token string
	Env   []string
}

// GitHubScope is where a GitHub repository's token goes: its git host.
const GitHubScope = "https://github.com/"

// tokenEnv gives git the token as basic auth's password, as GitHub takes it, through the environment
// (never arguments, which other processes can read) and only to the scope.
func tokenEnv(a Auth) []string {
	if a.Token == "" || a.Scope == "" {
		return a.Env
	}

	secret.Add(a.Token)

	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + a.Token))

	return append([]string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http." + a.Scope + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
		// a credential helper from the user's config would answer first
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
	}, a.Env...)
}

// AuthEnv is git's environment for a: the token as a header scoped to its URL, and the ssh command.
func AuthEnv(a Auth) []string { return tokenEnv(a) }

// OriginScope is repo's origin as its token's scope: an HTTPS URL without a user or password, or ""
// for any other (a git:// or file remote needs no token).
func OriginScope(ctx context.Context, repo string) string {
	out, err := git(ctx, repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}

	u, err := url.Parse(strings.TrimSpace(out))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}

	u.User = nil

	return u.String()
}

// BranchTip is the commit a remote's branch points at now, "" when the
// branch is gone.
func BranchTip(ctx context.Context, remote, branch string, auth Auth) (string, error) {
	out, err := gitEnv(ctx, "", tokenEnv(auth), "ls-remote", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}

	sha, _, _ := strings.Cut(out, "\t")

	return strings.TrimSpace(sha), nil
}

// Branches lists a remote's branches and their commits, with env (credential, ssh command) added:
// what a plain git connector picks its changes from.
func Branches(ctx context.Context, remote string, env []string) (map[string]string, error) {
	out, err := gitEnv(ctx, "", env, "ls-remote", "--heads", "--", remote)
	if err != nil {
		return nil, err
	}

	heads := map[string]string{}

	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if branch, isHead := strings.CutPrefix(ref, "refs/heads/"); ok && isHead {
			heads[branch] = sha
		}
	}

	return heads, nil
}

var ErrNoBranch = errors.New("no such branch on the remote")

// FetchTip reads a branch's tip from repo's origin and fetches it; the tip is the remote's answer,
// never FETCH_HEAD, which another step fetching in the same checkout may rewrite meanwhile.
func FetchTip(ctx context.Context, repo, branch string, auth Auth) (string, error) {
	out, err := gitEnv(ctx, repo, tokenEnv(auth), "ls-remote", "--", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}

	// ls-remote matches the pattern against the end of every ref name, so
	// only the line naming exactly the branch counts
	tip := ""

	for line := range strings.Lines(out) {
		if sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && ref == "refs/heads/"+branch {
			tip = sha
		}
	}

	if tip == "" {
		return "", fmt.Errorf("%s: %w", branch, ErrNoBranch)
	}

	if err := FetchCommit(ctx, repo, tip, auth); err != nil {
		return "", err
	}

	return tip, nil
}

func FetchCommit(ctx context.Context, repo, commit string, auth Auth) error {
	if _, err := git(ctx, repo, "cat-file", "-e", commit+"^{commit}"); err == nil {
		return nil
	}

	// FETCH_HEAD is the checkout's one shared file: a fetch by id needs
	// none of it
	_, err := gitEnv(ctx, repo, tokenEnv(auth), "fetch", "-q", "--no-write-fetch-head", "origin", commit)

	return err
}

// Checkout writes the tree under test into out: a git work tree of its own
// whose HEAD is commit, cloned from repo.
func Checkout(ctx context.Context, repo, commit, out string) error {
	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}

	abs, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("checkout: %w", err)
	}

	if _, err := git(ctx, out, "init", "-q"); err != nil {
		return err
	}

	if _, err := git(ctx, out, "fetch", "-q", "--no-tags", abs, commit); err != nil {
		if _, err := git(ctx, out, "fetch", "-q", "--no-tags", abs, treeRef); err != nil {
			return err
		}
	}

	_, err = git(ctx, out, "checkout", "-q", "--detach", commit)

	return err
}

func firstLine(s string) string {
	for i := range len(s) {
		if s[i] == '\n' {
			return s[:i]
		}
	}

	return s
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}

	return commit
}
