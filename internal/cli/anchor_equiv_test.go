package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The anchor runs on the work directory go run ./ci/proof leaves behind.
const (
	anchorWorkEnv    = "PIPELINEGEN_TEST_ANCHOR_WORK"
	anchorPluginsEnv = "PIPELINEGEN_TEST_ANCHOR_PLUGINS"
	anchorCasesEnv   = "PIPELINEGEN_TEST_ANCHOR_CASES"
)

var (
	anchorCases    = []string{"helper", "config", "start", "initialiser", "unused", "bump", "testfile", "both", "newdecl", "unrun", "signature", "width", "breaks"}
	anchorSettings = []string{"", "L3", "L5", "dependents", "dependents-L3"}
)

func TestTheModelBindsAsTheGoCorrelator(t *testing.T) {
	work := os.Getenv(anchorWorkEnv)
	if work == "" {
		t.Skipf("set %s to a plugin proof's work directory", anchorWorkEnv)
	}

	pdir := os.Getenv(anchorPluginsEnv)
	if pdir == "" {
		pdir = filepath.Join(work, "tools", "plugins")
	}

	fixture := filepath.Join(work, "fixture")

	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "caches"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(work, "cache", "caches", "gomodcache"), filepath.Join(cache, "caches", "gomodcache")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOMODCACHE", filepath.Join(cache, "caches", "gomodcache"))
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv(envGitURL, "file://"+filepath.Join(work, "remote"))

	a := anchor{t: t, fixture: fixture, plugins: pdir, cache: cache, seen: map[string]bool{}}

	recorded := a.rev("main")

	cases := anchorCases
	if only := os.Getenv(anchorCasesEnv); only != "" {
		cases = strings.Split(only, ",")
	}

	for _, c := range cases {
		for _, s := range anchorSettings {
			base, head := recorded, a.rev("change/"+c)
			if s != "" {
				base, head = a.rev("setting/"+s), a.rev("on/"+s+"/"+c)
			}

			label := c + "-" + s
			if s == "" {
				label = c + "-L2"
			}

			for _, commit := range []string{base, head} {
				a.compare(label, commit, recorded)
			}
		}
	}

	t.Logf("%d trees bound both ways, %d typed links in the last", a.trees, a.typed)

	if a.typed == 0 {
		t.Skipf("the Go correlator in %s answers no typed links; the model has nothing to bind over (last tree: %s)", pdir, a.last)
	}

	if len(a.gaps) > 0 {
		sort.Strings(a.gaps)
		t.Errorf("%d bindings differ between the Go correlator's bind and the model over its links:\n%s",
			len(a.gaps), strings.Join(a.gaps, "\n"))
	}
}

type anchor struct {
	t                       *testing.T
	fixture, plugins, cache string
	seen                    map[string]bool
	gaps                    []string
	trees, typed            int
	last                    string
}

func (a *anchor) rev(name string) string {
	a.t.Helper()

	out, err := exec.CommandContext(a.t.Context(), "git", "-C", a.fixture, "rev-parse", name).Output() //nolint:gosec // the proof's own fixture and branch names
	if err != nil {
		a.t.Fatalf("%s: %v", name, err)
	}

	return strings.TrimSpace(string(out))
}

func (a *anchor) compare(label, commit, recorded string) {
	t := a.t
	t.Helper()

	if a.seen[commit] {
		return
	}

	a.seen[commit] = true
	ctx := t.Context()

	dir := filepath.Join(t.TempDir(), "tree")
	if out, err := exec.CommandContext(ctx, "git", "-C", a.fixture, "worktree", "add", "-q", "--detach", dir, commit).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", commit, err, out)
	}

	defer func() {
		if err := exec.CommandContext(context.WithoutCancel(ctx), "git", "-C", a.fixture, "worktree", "remove", "--force", dir).Run(); err != nil {
			t.Logf("%s stays checked out: %v", dir, err)
		}
	}()

	cfg, err := config.Load(filepath.Join(dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	level := narrowingLevel(cfg)

	set, err := plugins.Load(ctx, plugins.Options{
		Config: cfg, Dir: a.plugins, Cache: a.cache, Answers: &plugincache.Cache{Dir: filepath.Join(a.cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	tree, err := set.Tree(ctx, dir, commit, level)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}

	ss := a.slices(ctx, cfg, recorded, set, level)

	// Binding adds units to the tree it binds in, so each binding gets its own copy.
	wantTree := anchorClone(tree)

	want, err := set.Bind(ctx, dir, wantTree, ss)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}

	// Both sides are compared as the sorted unbound names.
	for width, groups := range wantTree.Unbound {
		for group, ub := range groups {
			id := deps.UnboundID(width, group)
			if u, ok := wantTree.Units[id]; ok {
				u.Digest = strings.Join(slices.Sorted(slices.Values(ub.Names)), ",")
				wantTree.Units[id] = u
			}
		}
	}

	modelTree := anchorClone(tree)
	model := graphtest.Model{Plugins: graphPlugins(set), UnreachedNew: set.On("unreached-new", level), ReferencedNew: set.On("referenced-new", level)}
	got := model.Bind(modelTree, ss)

	a.trees++
	a.typed += len(tree.Typed)
	a.last = fmt.Sprintf("%d units, %d links, untold %v, plugin calls %+v", len(tree.Units), len(tree.Links), tree.Untold, set.Report())

	byKey := map[graphtest.Key]deps.Slice{}
	for _, d := range got {
		byKey[graphtest.KeyOf(d)] = d
	}

	for _, w := range want {
		g, ok := byKey[graphtest.KeyOf(w)]
		if !ok {
			a.gaps = append(a.gaps, fmt.Sprintf("  %s %s: the model binds no such slice", label, graphtest.KeyOf(w)))

			continue
		}

		if d := differ(dependsOn(wantTree, w), dependsOn(modelTree, g)); d != "" {
			a.gaps = append(a.gaps, fmt.Sprintf("  %s %s: %s", label, graphtest.KeyOf(w), d))
		}
	}
}

func (a *anchor) slices(ctx context.Context, cfg *config.Config, recorded string, set *plugins.Set, level int) []plugins.BindSlice {
	t := a.t

	store := openStore(cfg)
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()

	all, _, err := lists.For(ctx, store, a.fixture, recorded)
	if err != nil {
		t.Fatal(err)
	}

	var ss []plugins.BindSlice

	for _, key := range slices.Sorted(maps.Keys(all)) {
		suite, name, _ := strings.Cut(key, "/")
		ids := unitIDs(all[key])

		if name == recording.SetupList {
			if set.On(plugins.NarrowSetupSplit, level) {
				ss = append(ss, plugins.BindSlice{Suite: suite, Name: recording.SetupTarget(suite), List: ids, HasList: true, Setup: true})
			}

			continue
		}

		ss = append(ss, plugins.BindSlice{Suite: suite, Name: name, Sources: []string{name}, List: ids, HasList: true})
	}

	if len(ss) == 0 {
		t.Fatalf("no lists recorded on %s", recorded)
	}

	return ss
}

// The Go correlator binds a package whole as a group and its links bind declarations one by one;
// either way the slice depends on the same units.
func dependsOn(t *deps.Tree, d deps.Slice) map[string]string {
	out := map[string]string{}

	unit := func(id string) {
		if u, ok := t.Units[id]; ok {
			out[id] = u.Digest
		} else {
			out[id] = "absent"
		}
	}

	for _, id := range d.Units {
		unit(id)
	}

	// a set's input unit is not bound with its group whole.
	for id, u := range t.Units {
		if d.Everything || (slices.Contains(d.Groups, u.Group) && !slices.Contains(t.InputUnits, id)) {
			unit(id)
		}
	}

	for _, l := range d.Data {
		out["data "+l] = ""
	}

	return out
}

func differ(bind, model map[string]string) string {
	only := func(a, b map[string]string) []string {
		var out []string

		for id, digest := range a {
			if other, ok := b[id]; !ok || other != digest {
				out = append(out, id)
			}
		}

		slices.Sort(out)

		if len(out) > 3 {
			out = append(out[:3], fmt.Sprintf("and %d more", len(out)-3))
		}

		return out
	}

	b, m := only(bind, model), only(model, bind)
	if len(b) == 0 && len(m) == 0 {
		return ""
	}

	var names []string

	for id, digest := range bind {
		if other, ok := model[id]; ok && other != digest && strings.HasPrefix(id, "unbound:") {
			x, y := strings.Split(digest, ","), strings.Split(other, ",")
			names = append(names, fmt.Sprintf("%s: unbound for bind alone %v, for the model alone %v", id,
				slices.DeleteFunc(slices.Clone(x), func(n string) bool { return slices.Contains(y, n) }),
				slices.DeleteFunc(slices.Clone(y), func(n string) bool { return slices.Contains(x, n) })))
		}
	}

	slices.Sort(names)

	return fmt.Sprintf("bind alone %v, the model alone %v%s", b, m, strings.Join(append([]string{""}, names...), "; "))
}

func graphPlugins(set *plugins.Set) []graph.Plugin {
	var out []graph.Plugin

	for _, l := range set.Plugins {
		switch l.Desc.Kind {
		case plugin.KindSBOM, plugin.KindCorrelator:
			out = append(out, graph.Plugin{Name: l.Conf.Name, SBOM: l.Desc.Kind == plugin.KindSBOM, Namespaces: l.Desc.Namespaces})
		}
	}

	return out
}

func anchorClone(t *deps.Tree) *deps.Tree {
	c := *t
	c.Units = maps.Clone(t.Units)
	c.CannotTell = maps.Clone(t.CannotTell)
	c.Links = slices.Clone(t.Links)
	c.Typed = slices.Clone(t.Typed)

	return &c
}
