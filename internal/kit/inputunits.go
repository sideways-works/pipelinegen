package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errFixture = errors.New("the fixture's kit file")

const unitsCheck = "a set keyed on input units is the plugin's own while their digests hold"

// fixtureEdit is an edit the fixture names in its kit file: in path, the
// first old becomes new. An edit that leaves a unit's digest alone shows
// whether the sets keyed on it stay right, and one that changes it whether
// the digest covers what the links need.
type fixtureEdit struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func (k *kit) fixtureEdits() []fixtureEdit {
	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err != nil {
		return nil
	}

	var f struct {
		Edits []fixtureEdit `json:"edits"`
	}

	if json.Unmarshal(data, &f) != nil {
		return nil
	}

	return f.Edits
}

// inputUnits runs each of the fixture's edits through the core's cache: warm
// after the edit, the tree is the cold one, and the plugin is asked about no
// more files than the stale sets hold.
func (k *kit) inputUnits(ctx context.Context, sets []plugin.Set) {
	if !slices.ContainsFunc(sets, func(s plugin.Set) bool { return len(s.InputUnits) > 0 }) {
		k.skip(unitsCheck, "no set of the fixture names input units")

		return
	}

	if k.o.SBOM == "" {
		k.skip(unitsCheck, "no companion sbom (--sbom): the input units' digests are unknown")

		return
	}

	edits := k.fixtureEdits()
	if len(edits) == 0 {
		k.check(unitsCheck, "the fixture names no edits (%s: {\"edits\": [{\"path\", \"old\", \"new\"}]}), so no set keyed on input units was served after a change", kitFile)

		return
	}

	var problems []string

	for _, e := range edits {
		if p := k.inputUnitEdit(ctx, sets, e); p != "" {
			problems = append(problems, p)
		}
	}

	k.check(unitsCheck, strings.Join(problems, "; "))
}

func (k *kit) inputUnitEdit(ctx context.Context, sets []plugin.Set, e fixtureEdit) string {
	repo := k.work()
	if err := fixtureRepo(ctx, k.o.Tree, repo); err != nil {
		return fmt.Sprintf("a git copy of the fixture: %v", err)
	}

	answers := k.work()

	before, _, err := k.cachedTreeSet(ctx, repo, answers)
	if err != nil {
		return fmt.Sprintf("cold: %v", err)
	}

	if err := editCommit(ctx, repo, e); err != nil {
		return err.Error()
	}

	warm, set, err := k.cachedTreeSet(ctx, repo, answers)
	if err != nil {
		return fmt.Sprintf("warm after editing %s: %v", e.Path, err)
	}

	cold, err := k.cachedTree(ctx, repo, k.work())
	if err != nil {
		return fmt.Sprintf("cold after editing %s: %v", e.Path, err)
	}

	if treeText(warm) != treeText(cold) {
		return "after editing " + e.Path + ", the cache served other links than the plugin answers cold: an input unit's digest stays while what the links depend on changes" + k.hint()
	}

	owed := owedPaths(sets, before, cold, e.Path)

	for _, c := range set.Report().Calls {
		if c.Plugin == kitPlugin && c.Call == plugin.CallLinks && c.Asked > len(owed) {
			return fmt.Sprintf("after editing %s, asked about %d files, but only %s hold a changed file or input unit%s", e.Path, c.Asked, strings.Join(owed, ", "), k.hint())
		}
	}

	return ""
}

func owedPaths(sets []plugin.Set, before, after *deps.Tree, path string) []string {
	var out []string

	for _, s := range sets {
		changed := slices.Contains(s.Inputs, path) || slices.ContainsFunc(s.InputUnits, func(u string) bool {
			a, inBefore := before.Units[u]
			b, inAfter := after.Units[u]

			return inBefore != inAfter || a.Digest != b.Digest
		})

		if changed {
			out = append(out, s.Inputs...)
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func editCommit(ctx context.Context, dir string, e fixtureEdit) error {
	file := filepath.Join(dir, filepath.FromSlash(e.Path))

	data, err := os.ReadFile(file) //nolint:gosec // a file of the kit's own copy
	if err != nil {
		return fmt.Errorf("the edit of %s: %w", e.Path, err)
	}

	if !strings.Contains(string(data), e.Old) || e.Old == "" {
		return fmt.Errorf("%w: the edit of %s: %q isn't in it", errFixture, e.Path, e.Old)
	}

	if err := os.WriteFile(file, []byte(strings.Replace(string(data), e.Old, e.New, 1)), 0o600); err != nil { //nolint:gosec // as above
		return fmt.Errorf("the edit of %s: %w", e.Path, err)
	}

	return kitGit(ctx, dir, "commit", "-q", "-am", "edit")
}

func (k *kit) cachedTreeSet(ctx context.Context, repo, dir string) (*deps.Tree, *plugins.Set, error) {
	set, err := k.cachedSet(ctx, dir)
	if err != nil {
		return nil, nil, err
	}

	t, err := set.Tree(ctx, repo, "kit", config.DefaultNarrowing)
	k.copies.note(set.Report())

	return t, set, err
}
