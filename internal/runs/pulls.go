package runs

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/connector"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Changes lists a host's open proposed changes: the code connector's
// changes call.
type Changes interface {
	Changes(ctx context.Context, a plugin.ChangesArgs) ([]plugin.Change, error)
}

var errReady = errors.New("ready since")

// globEscaper makes a branch name a pattern that matches only itself.
var globEscaper = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

func changesArgs(pr *config.PullRequests, ready bool) plugin.ChangesArgs {
	return plugin.ChangesArgs{Base: pr.Base, Filters: plugin.ChangesFilters{
		Draft: pr.Draft.Wanted(), Labels: pr.Labels, LabelsWithout: pr.LabelsWithout, Head: pr.Head, ReadySince: ready,
	}}
}

// Admit says whether a pull request run plans this head (open onto its base, not a fork, passing its
// filters, and with once_per_ready unplanned since ready), and why not; ran finds a head's commit.
func Admit(ctx context.Context, host Changes, cfg *config.Config, ch Change, ran func(since time.Time) (string, error)) (bool, string, error) {
	pr := cfg.Runs[ch.Run].Trigger.PullRequests
	if pr == nil {
		return true, "", nil
	}

	args := changesArgs(pr, pr.OncePerReady)
	if pr.OncePerReady && ch.Branch != "" {
		// a timeline costs a request a pull request: ask after this one's
		args.Filters.Head = globEscaper.Replace(ch.Branch)
	}

	changes, err := host.Changes(ctx, args)
	if err != nil {
		return false, "", err
	}

	i := slices.IndexFunc(changes, func(c plugin.Change) bool { return c.Number == ch.PR })
	if i < 0 {
		return false, fmt.Sprintf("pull request #%d is closed, or no longer onto %s", ch.PR, pr.Base), nil
	}

	c := changes[i]

	if why := filtered(c, pr, ch.Commit); why != "" {
		return false, why, nil
	}

	if !pr.OncePerReady {
		return true, "", nil
	}

	if c.ReadySince == nil {
		return false, "", fmt.Errorf("%w: pull request #%d: the connector didn't say when it was last marked ready", errReady, c.Number)
	}

	other, err := ran(*c.ReadySince)
	if err != nil {
		return false, "", err
	}

	if other != "" {
		return false, fmt.Sprintf("pull request #%d ran at %s since it was last marked ready: set it to draft and ready again for a new run", c.Number, short(other)), nil
	}

	return true, "", nil
}

func filtered(c plugin.Change, pr *config.PullRequests, sha string) string {
	switch {
	case !connector.Admit(c):
		return fmt.Sprintf("pull request #%d doesn't come from the repository itself (%s): a fork's code never runs", c.Number, c.From)
	case c.Head != sha:
		return fmt.Sprintf("pull request #%d moved on to %s", c.Number, short(c.Head))
	case pr.Draft.Wanted() != nil && *pr.Draft.Wanted() != c.Draft:
		return fmt.Sprintf("pull request #%d is %s", c.Number, map[bool]string{true: "a draft", false: "ready"}[c.Draft])
	}

	for _, l := range pr.Labels {
		if !slices.Contains(c.Labels, l) {
			return fmt.Sprintf("pull request #%d lacks the label %s", c.Number, l)
		}
	}

	for _, l := range pr.LabelsWithout {
		if slices.Contains(c.Labels, l) {
			return fmt.Sprintf("pull request #%d has the label %s", c.Number, l)
		}
	}

	if pr.Head != "" {
		if ok, err := path.Match(pr.Head, c.HeadBranch); err != nil || !ok {
			return fmt.Sprintf("pull request #%d's branch %s isn't %s", c.Number, c.HeadBranch, pr.Head)
		}
	}

	return ""
}
