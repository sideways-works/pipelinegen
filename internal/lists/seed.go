package lists

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Seeds reads the seed notes among a lists ref's attached runs.
func Seeds(ctx context.Context, store *gitstore.Store, ref gitstore.Ref) ([]Attachment, error) {
	files, err := store.Under(ctx, ref.Commit, attached.Dir)
	if err != nil {
		return nil, err
	}

	var out []Attachment

	for _, name := range slices.Sorted(maps.Keys(files)) {
		var a Attachment
		if err := json.Unmarshal(files[name], &a); err != nil {
			return nil, fmt.Errorf("%w: %s in %s: %w", ErrLists, name, ref.Name, err)
		}

		if a.Seed != nil {
			out = append(out, a)
		}
	}

	return out, nil
}

// SeededBase is the newest of commits (newest first) whose lists were seeded
// or carry a baseline set by hand for run, and "" when none does: the base a
// branch's runs compare with before any of them passed.
func SeededBase(ctx context.Context, store *gitstore.Store, commits []string, run string) (string, error) {
	refs, err := store.List(ctx, RefPrefix)
	if err != nil {
		return "", err
	}

	for _, c := range commits {
		ref, ok := refs[Ref(c)]
		if !ok {
			continue
		}

		seeds, err := Seeds(ctx, store, ref)
		if err != nil {
			return "", err
		}

		if slices.ContainsFunc(seeds, func(a Attachment) bool { return !a.Seed.Baseline || a.Change == run }) {
			return c, nil
		}
	}

	return "", nil
}
