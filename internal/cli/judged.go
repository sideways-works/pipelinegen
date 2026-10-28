package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Planning counts a file an sbom plugin claims as covered, which the commit hook can't tell. The hook
// asks only for the describe answer, kept by the pinned checksum or from a binary whose SHA-256 is its pin.
func judgedByPlan(ctx context.Context, cfg *config.Config, dir, cache string, findings []check.Finding) []check.Finding {
	var (
		sboms   = map[string][]string{} // a plugin to the patterns it may claim
		unasked []string
	)

	for _, p := range cfg.Plugins {
		if p.Kind != config.KindSBOM {
			continue
		}

		d, err := pinnedDescribe(ctx, p, dir, cache)
		if err != nil {
			unasked = append(unasked, p.Name+" ("+err.Error()+")")

			continue
		}

		sboms[p.Name] = slices.Concat(d.Candidates, p.Include)
	}

	out := slices.Clone(findings)

	for i, f := range out {
		if f.Kind != check.KindUncovered || f.Warning || strings.HasSuffix(f.Path, "/") {
			continue
		}

		if name, ok := mayClaim(cfg, sboms, f.Path); ok {
			out[i].Warning = true
			out[i].Problem = "judged by the plan: " + name + " may claim it, and a file an sbom plugin claims is covered through its units"
			out[i].Fix = "nothing, if " + name + " claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested"

			continue
		}

		if len(unasked) > 0 {
			out[i].Fix += "; or fetch the plugins into " + dir + " so the check can ask " + strings.Join(unasked, ", ") + " what it may claim"
		}
	}

	return out
}

func mayClaim(cfg *config.Config, sboms map[string][]string, file string) (string, bool) {
	for _, p := range cfg.Plugins {
		patterns, ok := sboms[p.Name]
		if !ok || !matchAnyGlob(patterns, file) || matchAnyGlob(p.Exclude, file) {
			continue
		}

		return p.Name, true
	}

	return "", false
}

func matchAnyGlob(patterns []string, file string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return config.MatchGlob(p, file) })
}

// pinnedDescribe never runs a binary that isn't the pinned one.
func pinnedDescribe(ctx context.Context, p config.Plugin, dir, cache string) (plugin.Description, error) {
	var d plugin.Description

	if p.Release == nil || p.Release.Pin() == "" {
		return d, errUnpinned
	}

	// the answer may follow the plugin's config block too.
	conf, err := json.Marshal(p.Config)
	if err != nil {
		return d, fmt.Errorf("describe: %w", err)
	}

	pin := strings.ToLower(p.Release.Pin())
	confSum := sha256.Sum256(conf)
	kept := filepath.Join(cache, "describe", pin+"-"+hex.EncodeToString(confSum[:8])+".json")

	if data, err := os.ReadFile(kept); err == nil && json.Unmarshal(data, &d) == nil { //nolint:gosec // pipelinegen's own cache
		return d, nil
	}

	exe, err := pinnedBinary(p, dir)
	if err != nil {
		return d, err
	}

	// a machine that never ran pipelinegen has no cache directory yet.
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return d, fmt.Errorf("describe: %w", err)
	}

	work, err := os.MkdirTemp(cache, "describe-")
	if err != nil {
		return d, fmt.Errorf("describe: %w", err)
	}

	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	d, err = (plugin.Plugin{Name: p.Name, Path: exe, Config: p.Config}).Describe(ctx, work)
	if err != nil {
		return d, fmt.Errorf("describe: %w", err)
	}

	if data, err := json.Marshal(d); err == nil && os.MkdirAll(filepath.Dir(kept), 0o750) == nil {
		_ = os.WriteFile(kept, data, 0o600) //nolint:errcheck // a cache: the next run asks again
	}

	return d, nil
}

func pinnedBinary(p config.Plugin, dir string) (string, error) {
	if p.Release == nil || p.Release.Pin() == "" {
		return "", errUnpinned
	}

	exe := filepath.Join(dir, p.Name)

	sum, err := fileSHA256(exe)
	if err != nil {
		return "", errNotFetched
	}

	want, err := plugins.PinnedSum(p, dir)
	if err != nil {
		return "", err
	}

	if sum != want {
		return "", errNotPinned
	}

	return exe, nil
}

var (
	errUnpinned   = plugins.ErrUnpinned
	errNotFetched = errors.New("not fetched")
	errNotPinned  = errors.New("its binary is not the one its release pins")
)

var fileSHA256 = plugins.FileSHA256

// Claims are read from head's tree, checked out apart unless root's checkout is head, never from
// root's files, which are another tree's.
func sbomClaimed(ctx context.Context, cfg *config.Config, cache, dir, root, head string) (map[string]string, error) {
	only := *cfg
	only.Plugins = slices.DeleteFunc(slices.Clone(cfg.Plugins), func(p config.Plugin) bool { return p.Kind != config.KindSBOM })

	if len(only.Plugins) == 0 {
		return nil, nil //nolint:nilnil // no sbom, nothing claimed
	}

	repo := gitx.Repo{Dir: root}

	at, okAt, err := repo.Resolve(ctx, "HEAD")
	if err != nil {
		return nil, err
	}

	want, okWant, err := repo.Resolve(ctx, head)
	if err != nil {
		return nil, err
	}

	if !okWant {
		return nil, nil //nolint:nilnil // no commit, no tree to claim in
	}

	tree := root

	if !okAt || at != want {
		wt, remove, err := claimsTree(ctx, repo, cache, want)
		if err != nil {
			return nil, err
		}
		defer remove()

		tree = wt
	}

	work, done, err := callsDir(cache)
	if err != nil {
		return nil, err
	}
	defer done()

	set, err := plugins.Load(ctx, plugins.Options{Config: &only, Dir: dir, Cache: cache, Answers: answersCache(cache, nil), Work: work})
	if err != nil {
		return nil, err
	}

	notPinned.say(set)

	claims, err := set.Claims(ctx, tree)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}

	for _, p := range only.Plugins {
		for _, c := range claims[p.Name] {
			if _, ok := out[c.Path]; !ok {
				out[c.Path] = p.Name
			}
		}
	}

	return out, nil
}

// Two plans of one repository may run at once, so each gets its own directory.
func claimsTree(ctx context.Context, repo gitx.Repo, cache, commit string) (string, func(), error) {
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return "", nil, fmt.Errorf("claims: %w", err)
	}

	dir, err := os.MkdirTemp(cache, "claims-")
	if err != nil {
		return "", nil, fmt.Errorf("claims: %w", err)
	}

	// A cancelled context still leaves no worktree behind; what git fails to remove goes with the directory.
	remove := func() {
		bg := context.WithoutCancel(ctx)

		_, _ = repo.Git(bg, "worktree", "remove", "--force", dir) //nolint:errcheck // the directory goes below either way
		_ = os.RemoveAll(dir)                                     //nolint:errcheck // a temporary directory
		_, _ = repo.Git(bg, "worktree", "prune")                  //nolint:errcheck // tidying only
	}

	if _, err := repo.Git(ctx, "worktree", "add", "--force", "--detach", dir, commit); err != nil {
		remove()

		return "", nil, fmt.Errorf("claims: check out %s: %w", short12(commit), err)
	}

	return dir, remove, nil
}

// claimedOrNone falls back to nothing claimed, which only ever selects more.
func claimedOrNone(ctx context.Context, cfg *config.Config, cache, dir, root, head string) map[string]string {
	claimed, err := sbomClaimed(ctx, cfg, cache, dir, root, head)
	if err != nil {
		warnf(os.Stderr, "pipelinegen: what the sbom plugins claim is unknown, so their files count as the check finds them: %v\n", err)

		return nil
	}

	return claimed
}

func claimsIfUnknown(ctx context.Context, cfg *config.Config, cache, dir, root, head, known string) map[string]string {
	if known != "" {
		return nil
	}

	return claimedOrNone(ctx, cfg, cache, dir, root, head)
}

// Planning walks links, so a correlator offering bind and no links makes its recorded slices run on
// every change.
func unwalkableFindings(ctx context.Context, cfg *config.Config, dir, cache string) []check.Finding {
	if !outward() {
		return nil
	}

	var out []check.Finding

	for _, p := range cfg.Plugins {
		if p.Kind != config.KindCorrelator {
			continue
		}

		d, err := pinnedDescribe(ctx, p, dir, cache)
		if err != nil || !bindsWithoutLinks(d) {
			continue
		}

		version := d.Version
		if p.Release != nil {
			version = p.Release.Version
		}

		out = append(out, check.Finding{
			Kind:    check.KindBadConfig,
			Path:    config.FileName,
			Problem: noLinksText(p.Name, version),
			Fix:     "pin a release of " + p.Name + " whose describe lists links among its capabilities",
		})
	}

	return out
}
