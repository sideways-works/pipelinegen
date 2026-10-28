package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Only a trusted run's pipeline step writes to the store: a proposed change's plan runs the change's
// code, so what it computed must not be what later plans read.
func answersCache(cache string, store *gitstore.Store) *plugincache.Cache {
	// Only a step holding its job's link pushes: a generated pipeline's planning steps hold none and keep
	// their answers in their own cache.
	push := os.Getenv(envJobURL) != "" && (store == nil || !store.ReadOnly && !store.Proposed)

	answers := &plugincache.Cache{Dir: filepath.Join(cache, "answers"), Push: push}
	if store != nil {
		answers.Remote = &plugincache.Refs{Store: store}
	}

	return answers
}

const rebuildDir = "rebuild"

type rebuild struct {
	Found *affected.Rebuild

	answers *plugincache.Cache
	set     *plugins.Set
	done    func()
}

// Keeping the graph up to date rests on every answer depending on its declared inputs only; a cold
// rebuild is where a plugin that reads more shows. Nothing is written yet: the plan must hold what was
// found first.
func rebuildGraph(ctx context.Context, cfg *config.Config, store *gitstore.Store, trigger, head, cache, pluginsAt string) *rebuild {
	rb := &rebuild{Found: &affected.Rebuild{Check: cfg.RebuildCheck}, done: func() {}}

	if err := rb.build(ctx, cfg, store, trigger, head, cache, pluginsAt); err != nil {
		rb.Found.Error = err.Error()
	}

	return rb
}

func (rb *rebuild) build(ctx context.Context, cfg *config.Config, store *gitstore.Store, trigger, head, cache, pluginsAt string) error {
	work, done, err := callsDir(cache)
	if err != nil {
		return err
	}

	rb.done = done
	rb.answers = answersCache(cache, store)

	rb.set, err = plugins.Load(ctx, plugins.Options{Config: cfg, Dir: pluginsAt, Cache: cache, Answers: rb.answers, Work: work, Cold: true})
	if err != nil {
		return err
	}

	notPinned.say(rb.set)

	dir := filepath.Join(cache, rebuildDir)
	if err := (gitx.Repo{Dir: trigger}).Checkout(ctx, dir, head); err != nil {
		return err
	}

	_, err = rb.set.Tree(ctx, dir, head, narrowingLevel(cfg))

	// what was compared before an error still counts.
	rb.Found.Differences = rb.set.Differences()

	return err
}

func (rb *rebuild) say(w io.Writer) {
	level := "error"
	if rb.Found.Check == config.RebuildWarning {
		level = "warning"
	}

	if rb.Found.Error != "" {
		warnf(w, "pipelinegen plan: %s: the cold rebuild of the graph could not finish: %s\n", level, rb.Found.Error)
	}

	for _, d := range rb.Found.Differences {
		warnf(w, "pipelinegen plan: %s: the cold rebuild differs from the cache: %s %s, inputs %v, input units %v: %v\n", level, d.Plugin, d.Call, d.Inputs, d.InputUnits, d.What)
	}

	if rb.Found.Error == "" && len(rb.Found.Differences) == 0 {
		warnf(w, "pipelinegen plan: the cold rebuild of the graph equals the cache\n")
	}
}

// write stores the fresh answers only once the plan reporting them is stored; a rebuild that couldn't
// finish writes nothing.
func (rb *rebuild) write(ctx context.Context, w io.Writer) error {
	defer rb.done()

	if rb.set == nil || rb.Found.Error != "" {
		return nil
	}

	err := rb.set.Flush(ctx)

	var ce *plugincache.ConflictError
	if !errors.As(err, &ce) {
		return err
	}

	byBuild := map[plugincache.Build]map[string][]byte{}

	for _, c := range ce.Conflicts {
		if byBuild[c.Build] == nil {
			byBuild[c.Build] = map[string][]byte{}
		}

		byBuild[c.Build][c.Hash] = c.Got

		warnf(w, "pipelinegen plan: the cold rebuild replaces %s's stored answer %s\n", c.Build.Plugin, c.Hash)
	}

	if rb.answers.Remote == nil {
		return nil
	}

	for _, b := range slices.SortedFunc(maps.Keys(byBuild), compareBuild) {
		if err := rb.answers.Remote.Replace(ctx, b, byBuild[b]); err != nil {
			return fmt.Errorf("replace %s's answers: %w", b.Plugin, err)
		}
	}

	return nil
}

func compareBuild(a, b plugincache.Build) int {
	switch {
	case a.Plugin != b.Plugin:
		return compareStrings(a.Plugin, b.Plugin)
	case a.Version != b.Version:
		return compareStrings(a.Version, b.Version)
	default:
		return compareStrings(a.SHA256, b.SHA256)
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// The plan used its own answer, and the stored one stays: only the nightly's rebuild may replace it.
func conflictWarnings(set *plugins.Set, err error) error {
	var ce *plugincache.ConflictError
	if !errors.As(err, &ce) {
		return err
	}

	for _, c := range ce.Conflicts {
		set.Warnf("%s gave an answer that differs from the stored one for the same key %s: an input the key doesn't name, or an answer that isn't deterministic; this plan used its own, the stored one stays", c.Build.Plugin, c.Hash)
	}

	return nil
}
