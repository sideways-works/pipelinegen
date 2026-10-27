package kit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

const gitDir = ".git"

const cachedCheck = "answers served from the core's cache are the plugin's own"

// cached runs the plugin's units or links through the core's content cache
// as planning does, cold, warm, and warm after one claimed file changed,
// each compared with a cold answer for the same tree.
func (k *kit) cached(ctx context.Context, claimed []plugin.Claim) {
	// an input two sets share is the change that tells whether the
	// plugin answers every set holding it; else a claimed file, or for a
	// plugin that doesn't detect (it is handed every file) the first
	sets := k.answerSets(ctx, claimed)

	changed := sharedInput(sets)
	if changed != "" {
		k.everySetHolding(ctx, claimed, sets, changed)
	} else {
		k.skip(heldCheck, "no two sets of the fixture share an input")
	}

	if changed == "" && len(claimed) > 0 {
		changed = claimed[0].Path
	}

	if changed == "" {
		changed = firstFile(k.o.Tree)
	}

	if changed == "" {
		k.skip(cachedCheck, "the fixture has no file to change")

		return
	}

	repo := k.work()
	if err := fixtureRepo(ctx, k.o.Tree, repo); err != nil {
		k.check(cachedCheck, "a git copy of the fixture: %v", err)

		return
	}

	answers := k.work()

	steps := []struct {
		what  string
		edit  bool
		cache string
	}{
		{"warm", false, answers},
		{"warm after " + changed + " changed", true, answers},
	}

	before, err := k.cachedTree(ctx, repo, answers)
	if err != nil {
		k.check(cachedCheck, "cold: %v", err)

		return
	}

	for _, st := range steps {
		if st.edit {
			if err := appendCommit(ctx, repo, changed); err != nil {
				k.check(cachedCheck, "changing %s: %v", changed, err)

				return
			}
		}

		warm, err := k.cachedTree(ctx, repo, st.cache)
		if err != nil {
			k.check(cachedCheck, "%s: %v", st.what, err)

			return
		}

		cold, err := k.cachedTree(ctx, repo, k.work())
		if err != nil {
			k.check(cachedCheck, "cold, %s: %v", st.what, err)

			return
		}

		if a, b := treeText(warm), treeText(cold); a != b {
			k.check(cachedCheck, "%s, the cache served other units or links than the plugin answers cold%s", st.what, k.hint())

			return
		}

		if st.edit {
			k.endsChanged(before, cold, changed)
		}
	}

	k.check(cachedCheck, "")
}

const endsCheck = "a link changes only when one of its ends does"

// endsChanged notes the links that came or went while neither of their ends
// changed: planning counts the from end of such a link as changed all the
// same, so it costs selections, not correctness.
func (k *kit) endsChanged(before, after *deps.Tree, path string) {
	if k.desc.Kind == plugin.KindCorrelator && k.o.SBOM == "" {
		k.skip(endsCheck, "no companion sbom (--sbom): the digests of the links' ends are unknown")

		return
	}

	changed := func(id string) bool {
		if p, ok := strings.CutPrefix(id, "file:"); ok && p == path {
			return true
		}

		a, inBefore := before.Units[id]
		b, inAfter := after.Units[id]

		return inBefore != inAfter || a.Digest != b.Digest
	}

	was, is := treeLinks(before), treeLinks(after)

	var odd []string

	for _, d := range [2]struct {
		from, to map[string]plugin.Link
		what     string
	}{{was, is, "went"}, {is, was, "came"}} {
		for _, text := range slices.Sorted(maps.Keys(d.from)) {
			if _, ok := d.to[text]; ok {
				continue
			}

			if l := d.from[text]; !changed(l.From) && !changed(l.To) {
				odd = append(odd, d.what+": "+text)
			}
		}
	}

	if len(odd) == 0 {
		k.check(endsCheck, "")

		return
	}

	k.out = append(k.out, Result{
		Check: endsCheck, Note: true,
		Problem: "after " + path + " changed, links changed while neither end did, so planning selects their sources' slices too: " + strings.Join(firstFew(odd), "; "),
	})
}

func treeLinks(t *deps.Tree) map[string]plugin.Link {
	out := map[string]plugin.Link{}

	for _, l := range slices.Concat(t.Links, t.Typed) {
		out[linkText(l)] = l
	}

	for _, ruled := range []map[string][]plugin.Link{t.RuledOut, t.TypedRuledOut} {
		for n, links := range ruled {
			for _, l := range links {
				out["ruled out by "+n+": "+linkText(l)] = l
			}
		}
	}

	return out
}

func (k *kit) cachedTree(ctx context.Context, repo, dir string) (*deps.Tree, error) {
	t, _, err := k.cachedTreeSet(ctx, repo, dir)

	return t, err
}

const (
	kitPlugin = "plugin"
	kitSBOM   = "sbom"
)

func (k *kit) cachedSet(ctx context.Context, dir string) (*plugins.Set, error) {
	cfg := &config.Config{}

	bin := k.work()
	link := func(name, exe string) error {
		return os.Symlink(exe, filepath.Join(bin, name))
	}

	if k.o.SBOM != "" && k.desc.Kind == plugin.KindCorrelator {
		if err := link(kitSBOM, k.o.SBOM); err != nil {
			return nil, err
		}

		cfg.Plugins = append(cfg.Plugins, config.Plugin{Name: kitSBOM, Kind: plugin.KindSBOM, Config: k.o.SBOMConfig})
	}

	if err := link(kitPlugin, k.o.Path); err != nil {
		return nil, err
	}

	cfg.Plugins = append(cfg.Plugins, config.Plugin{Name: kitPlugin, Kind: k.desc.Kind, Config: k.o.Config})

	return plugins.Load(ctx, plugins.Options{
		Config: cfg, Dir: bin, Cache: filepath.Dir(k.p.Cache), Answers: &plugincache.Cache{Dir: dir},
		Work: k.work(), Described: map[string]plugin.Description{kitPlugin: k.desc},
	})
}

func treeText(t *deps.Tree) string {
	lines := make([]string, 0, len(t.Units)+len(t.Links)+len(t.CannotTell))

	for id, u := range t.Units {
		lines = append(lines, "unit "+id+" "+u.Digest)
	}

	for _, l := range slices.Concat(t.Links, t.Typed) {
		lines = append(lines, "link "+linkText(l))
	}

	for _, ruled := range []map[string][]plugin.Link{t.RuledOut, t.TypedRuledOut} {
		for n, links := range ruled {
			for _, l := range links {
				lines = append(lines, "ruled out by "+n+": link "+linkText(l))
			}
		}
	}

	for id, why := range t.CannotTell {
		lines = append(lines, "cannot tell "+id+" "+why)
	}

	slices.Sort(lines)

	return strings.Join(lines, "\n")
}

// linkText is every field of a link, so the cache comparison sees each.
func linkText(l plugin.Link) string {
	return fmt.Sprintf("%s>%s kind=%s narrowing=%s while=%s if_linked=%t ref=%t", l.From, l.To, l.Kind, l.Narrowing, l.While, l.IfLinked, l.Ref)
}

func (k *kit) answerSets(ctx context.Context, claimed []plugin.Claim) []plugin.Set {
	if k.desc.Kind == plugin.KindSBOM {
		ans, _ := k.units(ctx, nil, claimed)

		return ans.Sets
	}

	var ans plugin.LinksAnswer

	work := k.work()

	if k.call(ctx, plugin.CallLinks, plugin.TreeRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Claimed: claimed}, &ans) != nil {
		return nil
	}

	sets, err := plugin.ReadLines(work, named(ans.Files, "sets"), ans.Sets)
	if err != nil {
		return nil
	}

	return sets
}

const heldCheck = "an answer about a path holds every set that holds it"

// everySetHolding asks the plugin about one input several sets hold: its
// answer must hold each of them, as the protocol says.
func (k *kit) everySetHolding(ctx context.Context, claimed []plugin.Claim, cold []plugin.Set, path string) {
	var asked []plugin.Set

	if k.desc.Kind == plugin.KindSBOM {
		ans, _ := k.units(ctx, []string{path}, claimed)
		asked = ans.Sets
	} else {
		var ans plugin.LinksAnswer

		work := k.work()
		if err := k.call(ctx, plugin.CallLinks, plugin.TreeRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Paths: []string{path}, Claimed: claimed}, &ans); err != nil {
			k.check(heldCheck, "links about %s: %v", path, err)

			return
		}

		asked, _ = plugin.ReadLines(work, named(ans.Files, "sets"), ans.Sets) //nolint:errcheck // a set file that can't be read holds no set
	}

	var missing []string

	for _, set := range cold {
		if !slices.Contains(set.Inputs, path) {
			continue
		}

		if !slices.ContainsFunc(asked, func(a plugin.Set) bool { return slices.Equal(a.Inputs, set.Inputs) }) {
			missing = append(missing, set.Group)
		}
	}

	k.check(heldCheck, ifNot(len(missing) == 0, "asked about "+path+", it left out the sets of "+strings.Join(missing, ", ")))
}

func sharedInput(sets []plugin.Set) string {
	count := map[string]int{}

	for _, set := range sets {
		for _, in := range set.Inputs {
			count[in]++
		}
	}

	for _, in := range slices.Sorted(maps.Keys(count)) {
		if count[in] > 1 {
			return in
		}
	}

	return ""
}

func firstFile(tree string) string {
	first := ""

	_ = filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // a fixture that can't be walked has no first file
		if err != nil || first != "" {
			return filepath.SkipAll
		}

		if d.IsDir() {
			if d.Name() == gitDir {
				return filepath.SkipDir
			}

			return nil
		}

		if rel, _ := filepath.Rel(tree, p); rel != kitFile { //nolint:errcheck // p is under the tree
			first = filepath.ToSlash(rel)
		}

		return nil
	})

	return first
}

// fixtureRepo copies the fixture into dir as one commit: the core reads a
// tree's files from git.
func fixtureRepo(ctx context.Context, tree, dir string) error {
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(tree, p) //nolint:errcheck // p is under the tree

		if d.IsDir() {
			if d.Name() == gitDir {
				return filepath.SkipDir
			}

			return os.MkdirAll(filepath.Join(dir, rel), 0o750)
		}

		data, err := os.ReadFile(p) //nolint:gosec // the fixture's own file
		if err != nil {
			return err //nolint:wrapcheck // the check names the step
		}

		return os.WriteFile(filepath.Join(dir, rel), data, 0o600) //nolint:gosec // rel is a path the walk found under the fixture
	})
	if err != nil {
		return err //nolint:wrapcheck // as above
	}

	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture"}} {
		if err := kitGit(ctx, dir, args...); err != nil {
			return err
		}
	}

	return nil
}

func appendCommit(ctx context.Context, dir, path string) error {
	f, err := os.OpenFile(filepath.Join(dir, path), os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // a fixture file the plugin claimed
	if err != nil {
		return err //nolint:wrapcheck // the check names the step
	}

	_, werr := f.WriteString("\n")
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}

	return kitGit(ctx, dir, "commit", "-q", "-am", "change")
}

// kitGit runs git in the kit's own copy, as no one in particular and
// signing nothing.
func kitGit(ctx context.Context, dir string, args ...string) error {
	full := append([]string{"-c", "user.name=kit", "-c", "user.email=kit@example.invalid", "-c", "commit.gpgsign=false"}, args...)

	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // git with the kit's own arguments
	cmd.Dir = dir
	cmd.Env = secret.HardenGit(os.Environ())

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return nil
}
