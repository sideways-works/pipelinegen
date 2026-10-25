// Package plaingit is the built-in connector for a plain git server with no
// host API: its proposed changes are branches whose names match the config's
// patterns, listed with git itself. It reports nowhere; a run's state is in
// its records.
package plaingit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Name is the connector's plugin name in the config.
const Name = "git"

// IDPrefix starts every change id the connector gives: a branch, its name
// encoded as one ref part.
const IDPrefix = "branch-"

// ErrList is a listing the remote refused.
var ErrList = errors.New("list the remote's branches")

// Describe is what the connector can tell: changes, and nothing a host
// would keep (statuses, comments, drafts, labels, forks).
func Describe() plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindConnector, Name: Name, Version: "builtin",
		Capabilities: []string{plugin.CapChanges},
	}
}

// ChangeID is a branch's change id.
func ChangeID(branch string) string { return IDPrefix + report.RefPart(branch) }

// Changes lists the remote's branches whose names match one of patterns
// (path.Match), but base, as proposed changes onto base. Every one comes
// from the same repository: a plain git server has no forks. env is git's
// extra environment (the remote's credential, an ssh command).
func Changes(ctx context.Context, remote string, env []string, base string, patterns []string) ([]plugin.Change, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "--", remote) //nolint:gosec // git with the config's own remote
	cmd.Env = secret.GitEnv(env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s: %w: %s", ErrList, secret.Redact(remote), err, secret.Redact(strings.TrimSpace(stderr.String())))
	}

	var out []plugin.Change

	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		sha, ref, ok := strings.Cut(sc.Text(), "\t")

		branch, isHead := strings.CutPrefix(ref, "refs/heads/")
		if !ok || !isHead || branch == base || !matches(branch, patterns) {
			continue
		}

		out = append(out, plugin.Change{
			ID: ChangeID(branch), Base: base, Head: sha, HeadBranch: branch, From: plugin.FromSame,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].HeadBranch < out[j].HeadBranch })

	return out, nil
}

func matches(branch string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, branch); err == nil && ok {
			return true
		}
	}

	return false
}

// Links fills the config's page template for what is asked: {commit},
// {branch} and {path}. Without a template there is no page.
func Links(template string, a plugin.LinksArgs, branch string) plugin.WebLinks {
	if template == "" {
		return plugin.WebLinks{}
	}

	fill := func(commit, file string) string {
		return strings.NewReplacer("{commit}", commit, "{branch}", branch, "{path}", file).Replace(template)
	}

	var w plugin.WebLinks

	if a.Commit != "" {
		w.Commit = fill(a.Commit, "")
	}

	if a.File != "" {
		w.File = fill(a.Commit, a.File)
	}

	if a.Change != "" && branch != "" {
		w.Change = fill(a.Commit, "")
	}

	return w
}
