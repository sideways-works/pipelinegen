package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

var errNotTested = errors.New("the tree it tested can't be made again")

// carriedOver compares a pull request's slices from the tree its last build tested: a slice that
// passed there, or that build carried over, and that nothing from that tree to the head reaches,
// stays passed. Anything it can't tell runs, with a note for the plan's warnings.
func carriedOver(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, base, head, cache, bdPath string) (*affected.Rebase, []string) {
	offAt, on := passedSinceOn(ctx, cfg, trigger, base, head)

	if cfg.Runs[ch.Run].Select == config.SelectAll || store == nil || ch.ID == "" || base == "" || base == head || !on {
		return nil, nil
	}

	if everything, err := affected.Everything(ctx, affected.Options{Config: cfg, Repo: gitx.Repo{Dir: trigger}, Base: base, Head: head}); err != nil || everything != "" {
		return nil, nil
	}

	progress.Stage(ctx, "the slices the pull request's last build passed")

	b, ok, err := report.LastBuild(ctx, store, ch.Run, ch.ID, ch.Commit)
	if err != nil {
		return nil, []string{fmt.Sprintf("nothing is carried over from the last build of %s: %v", ch.ID, err)}
	}

	if !ok || len(b.Passes) == 0 {
		return nil, nil
	}

	from := b.Merged
	if from == "" {
		from = b.Commit
	}

	tested, err := testedTree(ctx, cfg, trigger, b)
	if err != nil {
		return nil, []string{fmt.Sprintf("nothing is carried over from %s, the last build of %s: %v", short12(from), ch.ID, err)}
	}

	reaches, err := reachFrom(ctx, cfg, store, trigger, ch.Run, tested, head, cache, bdPath, true)
	if errors.Is(err, errReachesAll) {
		return nil, nil
	}

	if err != nil {
		return nil, []string{fmt.Sprintf("nothing is carried over from %s, the last build of %s: what changed since could not be planned: %v", short12(from), ch.ID, err)}
	}

	why := fmt.Sprintf("carried over from %s, the last build of %s, where it passed: nothing from the tree that build tested (%s) to this one reaches it",
		short12(from), ch.ID, short12(tested))

	since := func(suite string, sl bazel.Slice) (affected.Since, bool) {
		p, ok := b.Passes[suite+"/"+sl.Name]
		if !ok || p.Label != "" && p.Label != sl.Label {
			return affected.Since{}, false
		}

		r, ok := reaches[suite]
		if !ok || r.all || r.slices[sl.Name] {
			return affected.Since{}, false
		}

		return affected.Since{Why: why, Sampled: r.sampled, CarriedFrom: from}, true
	}

	return &affected.Rebase{OffAt: offAt, Since: since}, nil
}

// passedSinceOn is the level passed-since is off from and whether it is on: under the change's config
// and, when the change edits it, under the base's too, as the plan unites the two.
func passedSinceOn(ctx context.Context, cfg *config.Config, trigger, base, head string) (int, bool) {
	offAt := passedSinceOffAt(cfg)
	on := narrowingLevel(cfg) < offAt

	byBase, changed, err := baseConfig(ctx, trigger, base, head)
	if err != nil {
		return offAt, false
	}

	if changed {
		offAt = min(offAt, passedSinceOffAt(byBase))
		on = on && narrowingLevel(byBase) < offAt
	}

	return offAt, on
}

// passedSinceOffAt is the level passed-since is off from: the config's own, else the core's.
func passedSinceOffAt(cfg *config.Config) int {
	if n, ok := cfg.Narrowings[plugins.NarrowPassedSince]; ok {
		return n.OffAt
	}

	return plugins.CoreOffAt(plugins.NarrowPassedSince)
}

// testedTree makes again the commit the build tested, its change merged into the target's tip it
// recorded, and holds it only when it is the very commit its plan tested.
func testedTree(ctx context.Context, cfg *config.Config, trigger string, b report.Build) (string, error) {
	if b.Merged == "" || !strings.HasPrefix(b.Merged, b.Commit) || !fullCommit.MatchString(b.Merged) || !fullCommit.MatchString(b.Base) || !fullCommit.MatchString(b.Head) {
		return "", fmt.Errorf("%w: its plan doesn't record the change's commit it merged", errNotTested)
	}

	auth := readAuth(ctx, cfg, trigger)

	for _, c := range []string{b.Merged, b.Base} {
		if err := runs.FetchCommit(ctx, trigger, c, auth); err != nil {
			return "", fmt.Errorf("%w: %s can't be fetched: %w", errNotTested, short12(c), err)
		}
	}

	merged, err := runs.MergeAgain(ctx, trigger, b.Merged, b.Base)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNotTested, err)
	}

	if merged != b.Head {
		return "", fmt.Errorf("%w: merging %s into %s gives %s, not the %s it tested", errNotTested, short12(b.Merged), short12(b.Base), short12(merged), short12(b.Head))
	}

	return merged, nil
}
