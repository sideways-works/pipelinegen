package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	narrowUnreachedNew  = "unreached-new"
	narrowReferencedNew = "referenced-new"
)

// fakeBinder is a bind correlator over the texts under pkgs/, each directory a group; a text's "=> t:x"
// lines reference x.
func fakeBinder(describe func(plugin.Description) plugin.Handler) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindCorrelator, Name: "binder",
			Capabilities: []string{plugin.CallBind},
			Narrowings: []plugin.Narrowing{
				{Name: narrowUnreachedNew, OffAt: 3, Note: "a new text nothing reaches selects nothing"},
				{Name: narrowReferencedNew, OffAt: 5, Note: "a text a touched one references is reached"},
			},
		}),
		plugin.CallBind: bindTexts,
	}
}

type binderRequest struct {
	Work       string   `json:"work"`
	Tree       string   `json:"tree"`
	Narrowings []string `json:"narrowings"`
	Slices     []struct {
		Suite string   `json:"suite"`
		Name  string   `json:"name"`
		List  []string `json:"list"`
	} `json:"slices"`
	Roots map[string][]string `json:"roots"`
	Setup map[string][]string `json:"setup"`
}

func bindTexts(in []byte) (any, error) {
	var req binderRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}

	groupOf, refs, err := readPkgs(req.Tree)
	if err != nil {
		return nil, err
	}

	unreachedNew := slices.Contains(req.Narrowings, narrowUnreachedNew)
	referencedNew := slices.Contains(req.Narrowings, narrowReferencedNew)

	bound := map[string]bool{}

	for _, ids := range req.Setup {
		for _, id := range ids {
			bound[id] = groupOf[id] != ""
		}
	}

	var records []map[string]any

	for _, sl := range req.Slices {
		var linked []string

		for _, id := range append(slices.Clone(sl.List), req.Roots[sl.Name]...) {
			if g := groupOf[id]; g != "" {
				bound[id] = true

				if !slices.Contains(linked, g) {
					linked = append(linked, g)
				}
			}
		}

		slices.Sort(linked)

		dropped := map[string][]string{}
		if unreachedNew {
			dropped[narrowUnreachedNew] = linked
		}

		if referencedNew {
			dropped[narrowReferencedNew] = linked
		}

		bindsLinked := linked
		if unreachedNew {
			bindsLinked = []string{}
		}

		for _, width := range []string{plugins.Executed, plugins.Dependents} {
			records = append(records, map[string]any{
				"suite": sl.Suite, "slice": sl.Name, "width": width, "units": [][]any{},
				"everything": len(sl.List) == 0, "linked": bindsLinked, "dropped_groups": dropped,
			})
		}
	}

	var unbound []map[string]any

	for _, g := range sortedValues(groupOf) {
		for _, width := range []string{plugins.Executed, plugins.Dependents} {
			names, only := []string{}, []string{}

			for _, id := range sortedKeys(groupOf) {
				switch {
				case groupOf[id] != g || bound[id]:
				case width == plugins.Executed && referencedNew && referenced(refs, bound, id):
					only = append(only, id)
				default:
					names = append(names, id)
				}
			}

			line := map[string]any{"group": g, "width": width, "names": names}
			if width == plugins.Executed && referencedNew {
				line["narrowed"] = map[string][]string{narrowReferencedNew: only}
			}

			unbound = append(unbound, line)
		}
	}

	files := map[string][]map[string]any{"units.jsonl": nil, "bind.jsonl": records, "unbound.jsonl": unbound}

	for name, rows := range files {
		var b strings.Builder

		for _, r := range rows {
			line, err := json.Marshal(r)
			if err != nil {
				return nil, fmt.Errorf("bind: %w", err)
			}

			b.Write(line)
			b.WriteByte('\n')
		}

		if err := os.WriteFile(filepath.Join(req.Work, name), []byte(b.String()), 0o600); err != nil {
			return nil, fmt.Errorf("bind: %w", err)
		}
	}

	return plugin.Answer{Files: sortedKeys(files)}, nil
}

func referenced(refs map[string][]string, bound map[string]bool, id string) bool {
	for from, tos := range refs {
		if bound[from] && slices.Contains(tos, id) {
			return true
		}
	}

	return false
}

func readPkgs(tree string) (map[string]string, map[string][]string, error) {
	groupOf, refs := map[string]string{}, map[string][]string{}

	err := filepath.WalkDir(filepath.Join(tree, "pkgs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".txt") {
			return err
		}

		data, err := os.ReadFile(path) //nolint:gosec // a fixture's file
		if err != nil {
			return fmt.Errorf("bind: %w", err)
		}

		rel, err := filepath.Rel(tree, path)
		if err != nil {
			return fmt.Errorf("bind: %w", err)
		}

		id := "t:" + strings.TrimSuffix(filepath.Base(path), ".txt")
		groupOf[id] = filepath.ToSlash(filepath.Dir(rel))

		for line := range strings.SplitSeq(string(data), "\n") {
			if to, ok := strings.CutPrefix(line, "=> "); ok {
				refs[id] = append(refs[id], to)
			}
		}

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bind: %w", err)
	}

	return groupOf, refs, nil
}

func sortedValues(m map[string]string) []string {
	var out []string

	for _, v := range m {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}

	slices.Sort(out)

	return out
}

func TestNarrowedIsWhatTheOffLevelAdds(t *testing.T) {
	testrepo.NeedBazel(t)
	t.Parallel()

	for _, group := range []int{0, 1, 2, 3, 4} {
		t.Run(strconv.Itoa(group), func(t *testing.T) {
			t.Parallel()
			testrepo.Slot(t)
			narrowedCases(t, group)
		})
	}
}

func narrowedCases(t *testing.T, group int) {
	t.Helper()

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("pkgs/p/BUILD.bazel", "filegroup(name = \"p\", srcs = glob([\"*.txt\"]))\n")
	r.Write("pkgs/q/BUILD.bazel", "filegroup(name = \"q\", srcs = glob([\"*.txt\"]))\n")
	r.Write("pkgs/p/f.txt", "f\n")
	r.Write("pkgs/p/g.txt", "g\n")
	r.Write("pkgs/p/u.txt", "u\n")
	r.Write("pkgs/q/h.txt", "h\n")
	r.Commit("pkgs")

	pdir := pluginsDirFor(t)
	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfgAt := func(level int) *config.Config {
		cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
		if err != nil {
			t.Fatal(err)
		}

		// A plugin entry changing in the config samples, as with the config under sample_on, rather than
		// running everything.
		cfg.EverythingOn = slices.DeleteFunc(cfg.EverythingOn, func(p string) bool { return p == config.FileName })
		cfg.SampleOn = append(cfg.SampleOn, config.FileName)
		cfg.Narrowing = level
		cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded, Sample: 1}
		cfg.Plugins = []config.Plugin{
			{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt", "pkgs/**/*.txt"}},
			{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
			{Name: "binder", Kind: config.KindCorrelator, Repository: "o/binder", Release: release, Include: []string{"pkgs/**/*.txt"}},
		}

		return cfg
	}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a", "t:k", "t:f"},
		"dspecs/specs/orders.spec.ts":   {"t:b", "t:k", "t:g", "t:h"},
		"dspecs/" + recording.SetupList: {"t:s"},
	}

	ex, dep := config.FollowExecuted, config.FollowDependents

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfgAt(2), Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A plan's width comes through its options, never its config, so a level's plans share snapshots (the
	// config is part of a snapshot's key).
	plan := func(cfg *config.Config, follow, base, head string) affected.SuitePlan {
		t.Helper()

		book := newPlanBook(set)

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
			Follow: func(string) string { return follow }, Narrowed: book.narrowedHook(), EntryPool: book.entryPool, Sample: sampleRule(cfg, set), Why: book.whyHook(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("everything: %s", p.Reason)
		}

		for _, sp := range p.Suites {
			if sp.Suite == "dspecs" {
				return sp
			}
		}

		t.Fatal("no dspecs in the plan")

		return affected.SuitePlan{}
	}

	// Level 5 is planned only where a case names it: every plan costs a hash of a tree.
	levels := []int{2, 3}
	offAt := map[string]int{
		"loose": 3, "setup-split": 3, plugins.NarrowSample: 3, plugins.NarrowSampleBound: 3,
		narrowUnreachedNew: 3, narrowReferencedNew: 5, plugins.NarrowPluginEntry: 4,
	}

	type change struct {
		group   int
		name    string
		edit    func()
		follows []string
		// want is what a plan selects by level ("2") or by level following dependents ("dep 2").
		want   map[string]string
		record map[string]string
		// pairOnly are, by "low>high", the slices only two narrowings switched off together select.
		pairOnly map[string][]string
	}

	appendTo := func(path string) func() {
		return func() { r.Write(path, readFile(t, filepath.Join(r.Dir, path))+"changed\n") }
	}

	const (
		all      = "specs/checkout.spec.ts,specs/fresh.spec.ts,specs/orders.spec.ts"
		fresh    = "specs/fresh.spec.ts"
		checkout = "specs/checkout.spec.ts,specs/fresh.spec.ts"
		orders   = "specs/fresh.spec.ts,specs/orders.spec.ts"
	)

	const (
		entryDrawn = checkout + "|" + orders
		entryLeft  = "narrowed plugin-entry:specs/orders.spec.ts of 3|narrowed plugin-entry:specs/checkout.spec.ts of 3"
	)

	// fresh has no list yet: every change selects it.
	cases := []change{
		{
			name: "a unit a slice touched", edit: appendTo("texts/a.txt"), follows: []string{ex, dep},
			want: map[string]string{"2": checkout, "3": checkout, "dep 3": all},
		},
		{
			name: "a unit reached through a link", edit: appendTo("texts/b.txt"), follows: []string{ex},
			want: map[string]string{"3": all},
		},
		{
			name: "the sbom's entry", follows: []string{ex, dep},
			edit: func() {
				r.Write("pipelinegen.yaml", readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))+
					"plugins:\n  - {name: texts, kind: sbom, repository: o/texts, release: {version: v1, sha256: "+strings.Repeat("b", 64)+"}}\n")
			},
			// Which recorded slice is the sample depends on the head commit, which differs from run to run.
			want: map[string]string{"2": entryDrawn, "3": entryDrawn, "5": all, "dep 2": entryDrawn, "dep 5": all},
			record: map[string]string{
				"2":     entryLeft,
				"dep 2": entryLeft,
			},
		},
		{
			group: 4, name: "a unit no slice reaches", edit: appendTo("texts/c.txt"), follows: []string{ex, dep},
			want: map[string]string{"2": fresh, "3": fresh, "dep 3": fresh},
		},
		{
			group: 4, name: "the correlator's entry", follows: []string{ex, dep},
			edit: func() {
				r.Write("pipelinegen.yaml", readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))+
					"plugins:\n  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v1, sha256: "+strings.Repeat("b", 64)+"}}\n")
			},
			want: map[string]string{"2": entryDrawn, "3": entryDrawn, "5": all, "dep 2": entryDrawn, "dep 5": all},
			record: map[string]string{
				"2":     entryLeft,
				"dep 2": entryLeft,
			},
		},
		{
			group: 4, name: "setup", edit: appendTo("texts/s.txt"), follows: []string{ex, dep},
			want: map[string]string{"2": fresh, "3": all, "dep 2": fresh},
			record: map[string]string{
				"2":     "sampled setup-split:specs/checkout.spec.ts,specs/orders.spec.ts of 3",
				"dep 2": "sampled setup-split:specs/checkout.spec.ts,specs/orders.spec.ts of 3",
			},
		},
		{
			group: 1, name: "what every slice ran", edit: appendTo("texts/k.txt"), follows: []string{ex},
			want:   map[string]string{"2": fresh, "3": all},
			record: map[string]string{"2": "sampled sample:specs/checkout.spec.ts,specs/orders.spec.ts of 3"},
		},
		{
			group: 1, name: "what every slice binds only through it", edit: appendTo("texts/m.txt"), follows: []string{ex},
			want:   map[string]string{"2": fresh, "3": all},
			record: map[string]string{"2": "sampled sample-bound:specs/checkout.spec.ts,specs/orders.spec.ts of 3"},
		},
		{
			group: 1, name: "a unit a slice reaches through its own code", edit: appendTo("texts/n.txt"), follows: []string{ex},
			want: map[string]string{"2": all},
		},
		{
			group: 1, name: "a ruled-out link", edit: appendTo("texts/d.txt"), follows: []string{ex},
			want:   map[string]string{"2": fresh, "3": checkout},
			record: map[string]string{"2": "narrowed loose:specs/checkout.spec.ts of 2"},
		},
		{
			group: 2, name: "a new text nothing reaches in a group two slices link", follows: []string{ex, dep},
			edit: func() { r.Write("pkgs/p/new.txt", "new\n") },
			want: map[string]string{"2": fresh, "3": all, "dep 3": all},
			record: map[string]string{
				"2":     "narrowed unreached-new:specs/checkout.spec.ts,specs/orders.spec.ts of 3",
				"dep 2": "narrowed unreached-new:specs/checkout.spec.ts,specs/orders.spec.ts of 3",
			},
		},
		{
			group: 2, name: "a new text nothing reaches in a group one slice links", follows: []string{ex},
			edit: func() { r.Write("pkgs/q/other.txt", "other\n") },
			want: map[string]string{"3": orders},
		},
		{
			group: 2, name: "a changed text a slice touched", follows: []string{ex}, edit: appendTo("pkgs/p/f.txt"),
			want:   map[string]string{"2": checkout, "3": checkout},
			record: map[string]string{"2": ""},
		},
		{
			group: 3, name: "a changed text nothing reaches", follows: []string{ex}, edit: appendTo("pkgs/p/u.txt"),
			want: map[string]string{"2": fresh, "3": fresh, "5": fresh},
		},
		{
			group: 3, name: "a new text a touched one references", follows: []string{ex},
			edit: func() {
				r.Write("pkgs/p/g.txt", readFile(t, filepath.Join(r.Dir, "pkgs/p/g.txt"))+"=> t:helper\n")
				r.Write("pkgs/p/helper.txt", "helper\n")
			},
			want:     map[string]string{"3": orders, "5": all},
			record:   map[string]string{"2": "", "3": "narrowed referenced-new:specs/checkout.spec.ts of 3"},
			pairOnly: map[string][]string{"2>5": {"specs/checkout.spec.ts"}},
		},
		{
			group: 3, name: "a text nothing reaches goes", follows: []string{ex},
			edit: func() { r.Git("rm", "-q", "pkgs/p/u.txt") },
			want: map[string]string{"3": all},
		},
	}

	for _, c := range cases {
		if c.group != group {
			continue
		}

		base := r.Git("rev-parse", "HEAD")

		c.edit()
		head := r.Commit(c.name)

		at := levels
		if _, ok := c.want["5"]; ok {
			at = append(slices.Clone(levels), 5)
		}

		for _, follow := range c.follows {
			plans := map[int]affected.SuitePlan{}
			for _, level := range at {
				plans[level] = plan(cfgAt(level), follow, base, head)
			}

			name := c.name + " (" + follow + ")"

			key := func(level int) string {
				if follow == dep {
					return "dep " + strconv.Itoa(level)
				}

				return strconv.Itoa(level)
			}

			for _, level := range at {
				// a want of alternatives takes any one of them, and a
				// record of alternatives the one at the same place.
				drawn := -1

				if want, ok := c.want[key(level)]; ok {
					got := selectedNames(plans[level])
					if drawn = slices.Index(strings.Split(want, "|"), got); drawn < 0 {
						t.Errorf("%s at %d: selected %q, want %q", name, level, got, want)
					}
				}

				if want, ok := c.record[key(level)]; ok {
					alts := strings.Split(want, "|")
					got := records(plans[level])

					if i := slices.Index(alts, got); i < 0 || drawn >= 0 && len(alts) > 1 && i != drawn {
						t.Errorf("%s at %d: narrowed %q, want %q", name, level, got, want)
					}
				}
			}

			if c.name == "a unit reached through a link" {
				for spec, want := range map[string]string{
					"specs/orders.spec.ts":   "executed t:b []",
					"specs/checkout.spec.ts": "depends t:b [t:a]",
				} {
					var got []string
					for _, r := range plans[3].Reasons["@pipelinegen_deps//slices:"+spec] {
						got = append(got, fmt.Sprintf("%s %s %v", r.Kind, r.What, r.Through))
					}

					if !slices.Contains(got, want) {
						t.Errorf("%s: reasons of %s %v, want %q among them", name, spec, got, want)
					}
				}
			}

			for i, lo := range at {
				for _, hi := range at[i+1:] {
					var named []string

					for _, n := range plans[lo].Narrowed {
						off, ok := offAt[n.Narrowing]
						if !ok {
							t.Fatalf("%s: a record of %s, which the test doesn't know", name, n.Narrowing)
						}

						if lo < off && off <= hi {
							named = append(named, n.Dropped...)
						}
					}

					slices.Sort(named)
					named = slices.Compact(named)

					var added []string

					for _, sl := range plans[hi].Selected {
						if !slices.ContainsFunc(plans[lo].Selected, func(o bazel.Slice) bool { return o.Name == sl.Name }) {
							added = append(added, sl.Name)
						}
					}

					slices.Sort(added)

					added = slices.DeleteFunc(added, func(name string) bool {
						return slices.Contains(c.pairOnly[fmt.Sprintf("%d>%d", lo, hi)], name)
					})

					// level 5 also follows dependents, which no narrowing records: against it a plan
					// following executed names a part of what it adds.
					exact := follow != ex || hi < 5
					if exact && !slices.Equal(named, added) || !exact && !subset(named, added) {
						t.Errorf("%s: level %d names %v as left out by narrowings off up to %d, which selects %v more (records %+v)", name, lo, named, hi, added, plans[lo].Narrowed)
					}
				}
			}
		}
	}
}

func subset(a, b []string) bool {
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}

	return true
}

func selectedNames(sp affected.SuitePlan) string {
	out := make([]string, 0, len(sp.Selected))
	for _, sl := range sp.Selected {
		out = append(out, sl.Name)
	}

	return strings.Join(out, ",")
}

func records(sp affected.SuitePlan) string {
	out := make([]string, 0, len(sp.Narrowed))
	for _, n := range sp.Narrowed {
		out = append(out, fmt.Sprintf("%s %s:%s of %d", n.Kind, n.Narrowing, strings.Join(n.Dropped, ","), n.Reached))
	}

	return strings.Join(out, " ")
}
