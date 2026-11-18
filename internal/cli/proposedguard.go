package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errWritingCredential = errors.New("a step that runs a proposed change's code holds a credential that writes")

const singleTokenText = "this pipeline gives a pull request's code a token that can write: use tokens by purpose " +
	"(github.tokens, the read token for planning a pull request); the next release refuses it"

func splitCredentials(cfg *config.Config) bool {
	keys := cfg.Git.SSHKey

	return cfg.GitHub.SplitTokens() != nil || keys.Store != "" && keys.Store != keys.Read
}

var hostTokens = []string{toolcalls.EnvToken, "GH_TOKEN"}

// The tool can't know a token's scope, so it goes by the planner's declaration.
func declaredForPlanner(cfg *config.Config, name string) bool {
	p := cfg.Concourse.Planner
	_, secret := p.Secrets[name]

	return p.GitToken == name || secret
}

func writingVars(cfg *config.Config) []string {
	names := make([]string, 0, 4)
	for _, p := range []string{plugin.PurposeChanges, plugin.PurposeReport, plugin.PurposeStore} {
		names = append(names, toolcalls.PurposeEnv(p))
	}

	if cfg.Git.SSHKey.Store != "" {
		names = append(names, gitssh.KeyEnv(plugin.PurposeStore))
	}

	return names
}

func held(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return os.Getenv(n) == "" })
}

// guardProposed holds a step that runs a proposed change's code to the read credential; with one token
// every step holds it, so the step plans and first says what that token is exposed to.
func guardProposed(cfg *config.Config) (lines []string, err error) {
	refused := held(writingVars(cfg))

	split := splitCredentials(cfg)

	for _, name := range held(hostTokens) {
		switch {
		case !split:
			lines = append(lines, singleTokenText)
		case declaredForPlanner(cfg, name):
			lines = append(lines, declaredText(name))
		default:
			refused = append(refused, name)
		}
	}

	if len(refused) > 0 {
		return nil, fmt.Errorf("%w: %s; a pull request's code runs here, so this step may hold the read credential alone: "+
			"regenerate the pipeline with a runner that gives its planning the read token only", errWritingCredential, strings.Join(refused, ", "))
	}

	return slices.Compact(lines), nil
}

func declaredText(name string) string {
	return "the planning task holds " + name + " as its declared git credential: make sure it can only read"
}

func singleTokenFinding(cfg *config.Config) []check.Finding {
	if !slices.ContainsFunc(cfg.RunNames(), func(r string) bool { return cfg.Runs[r].Trigger.PullRequests != nil }) {
		return nil
	}

	if splitCredentials(cfg) {
		var out []check.Finding

		for _, name := range hostTokens {
			if declaredForPlanner(cfg, name) {
				out = append(out, check.Finding{
					Kind: check.KindBadConfig, Path: config.FileName, Note: true, Problem: declaredText(name),
					Fix: "give the planner the read token under " + name + ", one that can't write",
				})
			}
		}

		return out
	}

	return []check.Finding{{
		Kind: check.KindBadConfig, Path: config.FileName, Warning: true,
		Problem: singleTokenText,
		Fix:     "give each purpose its own token (github.tokens: {read, changes, report, store}), the read one a token that only reads",
	}}
}

func proposedWarning(cfg *config.Config, store *gitstore.Store) ([]string, error) {
	if !store.Proposed {
		return nil, nil
	}

	return guardProposed(cfg)
}

func sayFirst(plan *affected.Plan, lines []string) {
	for _, l := range lines {
		warnf(os.Stderr, "pipelinegen plan: %s\n", l)
	}

	plan.Warnings = append(slices.Clone(lines), plan.Warnings...)
}

const refusedNext = "; the next release refuses it"

// With tokens by purpose the config is refused for such tasks, so these lines come only with one token.
func writingSecretLines(cfg *config.Config, run string) []string {
	lines := cfg.WritingSecrets(run)
	for i := range lines {
		lines[i] += refusedNext
	}

	return lines
}

func firstWarnings(cfg *config.Config, run string, guard []string) []string {
	return append(writingSecretLines(cfg, run), guard...)
}

func writingSecretFindings(cfg *config.Config) []check.Finding {
	lines := cfg.WritingSecrets("")
	out := make([]check.Finding, 0, len(lines))

	for _, line := range lines {
		out = append(out, check.Finding{
			Kind: check.KindBadConfig, Path: config.FileName, Warning: true, Problem: line + refusedNext,
			Fix: "split the token by purpose (github.tokens) and give the task the read token under the same variable",
		})
	}

	return out
}
