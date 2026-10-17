package affected

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

type walkCase struct {
	walk     map[string][]graph.Hit // by width
	inputs   map[string]map[string][]string
	changes  []string
	impacted []string
	hashed   []string
	added    bool // head has a fourth test file
	follow   string
}

const (
	commitBase = "base"
	commitHead = "head"
)

func walkSlice(name string) bazel.Slice {
	return bazel.Slice{Name: name, Label: bazel.SelectorLabel(name), Sources: []string{name}}
}

func (c walkCase) plan(t *testing.T, walked bool) ([]SuitePlan, []string) {
	t.Helper()

	cfg := &config.Config{Suites: map[string]config.Suite{
		"rec":   {Tests: []string{"rec/*_test.go"}, Bind: config.BindRecorded},
		"graph": {Tests: []string{"graph/*_test.go"}, Bind: config.BindGraph},
	}}

	recorded := []bazel.Slice{walkSlice("rec/a_test.go"), walkSlice("rec/b_test.go"), walkSlice("rec/c_test.go")}
	graph := []bazel.Slice{walkSlice("graph/g_test.go")}

	head := slices.Clone(recorded)
	if c.added {
		head = append(head, walkSlice("rec/d_test.go"))
	}

	dir := t.TempDir()
	hashes := filepath.Join(dir, "head.hashes")

	var js []string
	for _, l := range c.hashed {
		js = append(js, `"`+l+`": "x"`)
	}

	if err := os.WriteFile(hashes, []byte("{"+strings.Join(js, ",")+"}"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := &Snapshot{Commit: commitBase, Slices: map[string][]bazel.Slice{"rec": recorded, "graph": graph}}
	h := &Snapshot{Commit: commitHead, Slices: map[string][]bazel.Slice{"rec": head, "graph": graph}, Hashes: hashes}

	var warnings []string

	o := Options{
		Config:  cfg,
		Warn:    func(format string, _ ...any) { warnings = append(warnings, format) },
		Reasons: func(_, _, target string) ([]Reason, bool) { return []Reason{{Kind: KindTarget, What: target}}, true },
		Prepare: func(context.Context, string, string, map[string][]bazel.Slice) (string, error) { return "", nil },
	}

	if c.follow != "" {
		o.Follow = func(string) string { return c.follow }
	}

	if walked {
		o.Select = c.selection("rec")
		o.Inputs = func(commit, _, slice string) ([]string, error) { return c.inputs[commit][slice], nil }
	}

	impacted := map[string]bool{}
	for _, l := range c.impacted {
		impacted[bazel.NormalLabel(l)] = true
	}

	var changes []gitx.Change
	for _, p := range c.changes {
		changes = append(changes, gitx.Change{Path: p})
	}

	sps, err := selectSuites(t.Context(), o, b, h, impacted, changes)
	if err != nil {
		t.Fatal(err)
	}

	return sps, warnings
}

func suitePlan(t *testing.T, sps []SuitePlan, name string) SuitePlan {
	t.Helper()

	for _, sp := range sps {
		if sp.Suite == name {
			return sp
		}
	}

	t.Fatalf("no suite %s", name)

	return SuitePlan{}
}

func names(sp SuitePlan) string {
	var out []string
	for _, sl := range sp.Selected {
		out = append(out, sl.Name)
	}

	return strings.Join(out, ",")
}

func TestWalkedSelectionEachSource(t *testing.T) {
	a := "rec/a_test.go"
	cases := []struct {
		name string
		c    walkCase
		want string
		kind string
		what string
	}{
		{"an empty commit", walkCase{}, "", "", ""},
		{"nothing", walkCase{hashed: []string{"//data:a"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//data:a"}}, commitHead: {a: {"//data:a"}},
		}}, "", "", ""},
		{"the walk", walkCase{walk: map[string][]graph.Hit{config.FollowExecuted: {{Slice: a, Paths: []graph.Path{{Listed: "go:x#func:F", Changed: "go:x#func:F"}}}}}}, a, KindExecuted, "go:x#func:F"},
		{"its own file", walkCase{changes: []string{a}}, a, KindTestChanged, a},
		{"an impacted input", walkCase{impacted: []string{"//data:a"}, hashed: []string{"//data:a"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//data:a"}}, commitHead: {a: {"//data:a"}},
		}}, a, KindTarget, "//data:a"},
		{"an input the head gained", walkCase{hashed: []string{"//data:a", "//data:b"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//data:a"}}, commitHead: {a: {"//data:a", "//data:b"}},
		}}, a, KindInputAdded, "//data:b"},
		{"an input the head lost", walkCase{hashed: []string{"//data:a"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//data:a", "//data:b"}}, commitHead: {a: {"//data:a"}},
		}}, a, KindInputRemoved, "//data:b"},
		{"an input no hash holds", walkCase{changes: []string{"other/file.txt"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"@ext//x:y"}}, commitHead: {a: {"@ext//x:y"}},
		}}, a, KindInputUnknown, "@ext//x:y"},
		{"an empty commit, with an input no hash holds", walkCase{inputs: map[string]map[string][]string{
			commitBase: {a: {"@ext//x:y"}}, commitHead: {a: {"@ext//x:y"}},
		}}, "", "", ""},
		{"an empty commit, with a short label", walkCase{hashed: []string{"//web:web"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//web"}}, commitHead: {a: {"//web"}},
		}}, "", "", ""},
		{"a change elsewhere, with a short label", walkCase{changes: []string{"other/file.txt"}, hashed: []string{"//web:web"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"//web"}}, commitHead: {a: {"//web"}},
		}}, "", "", ""},
		{"labels in any form", walkCase{impacted: []string{"@@//data:a"}, hashed: []string{"//data:a"}, inputs: map[string]map[string][]string{
			commitBase: {a: {"@//data:a"}}, commitHead: {a: {"//data:a"}},
		}}, a, KindTarget, "//data:a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sps, _ := tc.c.plan(t, true)
			sp := suitePlan(t, sps, "rec")

			if got := names(sp); got != tc.want {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}

			if tc.want == "" {
				return
			}

			rs := sp.Reasons[bazel.SelectorLabel(tc.want)]
			if !slices.ContainsFunc(rs, func(r Reason) bool { return r.Kind == tc.kind && r.What == tc.what }) {
				t.Fatalf("reasons %+v, want %s %s", rs, tc.kind, tc.what)
			}
		})
	}
}

func TestWalkedNewSlice(t *testing.T) {
	sps, _ := walkCase{added: true}.plan(t, true)
	sp := suitePlan(t, sps, "rec")

	if got := names(sp); got != "rec/d_test.go" {
		t.Fatalf("selected %q", got)
	}

	if rs := sp.Reasons[bazel.SelectorLabel("rec/d_test.go")]; len(rs) != 1 || rs[0].Kind != KindNewSlice {
		t.Fatalf("reasons %+v", rs)
	}
}

func TestWalkedUnhashedInputWarns(t *testing.T) {
	_, warnings := walkCase{changes: []string{"other/file.txt"}, inputs: map[string]map[string][]string{
		commitBase: {"rec/a_test.go": {"@ext//x:y"}}, commitHead: {"rec/a_test.go": {"@ext//x:y"}},
	}}.plan(t, true)

	if len(warnings) != 1 || !strings.Contains(warnings[0], "can't be told unchanged") {
		t.Fatalf("warnings %q", warnings)
	}
}

func TestWalkedAtTheSuitesWidth(t *testing.T) {
	a := "rec/a_test.go"
	c := walkCase{
		follow:   config.FollowDependents,
		walk:     map[string][]graph.Hit{config.FollowDependents: {{Slice: a}}, config.FollowExecuted: {{Slice: "rec/b_test.go"}}},
		impacted: []string{bazel.SelectorLabel("rec/c_test.go"), bazel.SelectorLabel("rec/c_test.go") + dependentsSuffix},
	}

	sps, _ := c.plan(t, true)

	if got := names(suitePlan(t, sps, "rec")); got != a {
		t.Fatalf("selected %q, want %s", got, a)
	}
}

func TestWalkedSuiteTargets(t *testing.T) {
	setup := walkCase{walk: map[string][]graph.Hit{config.FollowExecuted: {{Slice: recording.SetupTarget("rec")}}}}

	sps, _ := setup.plan(t, true)
	if sp := suitePlan(t, sps, "rec"); !sp.All || len(sp.Selected) != 3 || !strings.Contains(sp.Reason, "setup") {
		t.Fatalf("setup: %+v", sp)
	}

	sample := walkCase{walk: map[string][]graph.Hit{config.FollowExecuted: {{Slice: SampleTarget("rec")}}}}

	sps, _ = sample.plan(t, true)
	if sp := suitePlan(t, sps, "rec"); len(sp.Selected) != 0 {
		t.Fatalf("sample off: %+v", sp)
	}

	j, err := Options{Config: &config.Config{Suites: map[string]config.Suite{"rec": {Tests: []string{"x"}, Bind: config.BindRecorded}}},
		Select: walkCase{walk: map[string][]graph.Hit{config.FollowExecuted: {{Slice: SampleTarget("rec")}}}}.selection("rec"),
	}.join(t.Context(), "rec", &Snapshot{}, &Snapshot{}, nil, nil, hashedLabels(""))
	if err != nil {
		t.Fatal(err)
	}

	if !j.seen[bazel.SelectorLabel(SampleTarget("rec"))] || j.seen[bazel.SelectorLabel(SampleBoundTarget("rec"))] {
		t.Fatalf("sample targets: %v", j.seen)
	}
}

func TestWalkedTestFileSuites(t *testing.T) {
	g := "graph/g_test.go"
	c := walkCase{
		walk:     map[string][]graph.Hit{config.FollowExecuted: {{Slice: "rec/a_test.go"}}},
		impacted: []string{bazel.SelectorLabel(g), bazel.SelectorLabel("rec/b_test.go"), "//graph:g"},
		hashed:   []string{"//graph:g"},
		inputs: map[string]map[string][]string{
			commitBase: {g: {"//graph:g"}}, commitHead: {g: {"//graph:g"}},
		},
	}

	on, _ := c.plan(t, true)
	off, _ := c.plan(t, false)

	gp := suitePlan(t, on, "graph")
	if names(gp) != g || gp.Reasons[bazel.SelectorLabel(g)][0].What != "//graph:g" {
		t.Fatalf("graph suite walked: %+v", gp)
	}

	if names(suitePlan(t, off, "graph")) != g {
		t.Fatal("graph suite by its selector")
	}

	if got := names(suitePlan(t, off, "rec")); got != "rec/b_test.go" {
		t.Fatalf("without the walk the selectors select: %q", got)
	}

	if got := names(suitePlan(t, on, "rec")); got != "rec/a_test.go" {
		t.Fatalf("with the walk: %q", got)
	}

	c.walk = map[string][]graph.Hit{config.FollowExecuted: {{Slice: g}}}
	c.impacted = []string{bazel.SelectorLabel(g)}

	if on, _ = c.plan(t, true); names(suitePlan(t, on, "graph")) != g {
		t.Fatal("graph suite by the walk")
	}

	c.walk = nil

	if on, _ = c.plan(t, true); names(suitePlan(t, on, "graph")) != "" {
		t.Fatal("a selector selects nothing once the suite is walked")
	}

	tagged := Options{Select: c.selection("t"), Config: &config.Config{Suites: map[string]config.Suite{
		"t": {Tag: "slice", Tests: []string{"x"}, Bind: config.BindRecorded},
	}}}
	if tagged.walks("t") {
		t.Fatal("a tagged suite is its own targets' and never walked")
	}
}

func (c walkCase) selection(suite string) *graph.Selection {
	s := &graph.Selection{Selected: map[graph.Key][]graph.Path{}}

	for width, hits := range c.walk {
		for _, h := range hits {
			in := suite
			if dir, _, ok := strings.Cut(h.Slice, "/"); ok {
				in = dir
			}

			s.Selected[graph.Key{Suite: in, Name: h.Slice, Width: width}] = h.Paths
		}
	}

	return s
}

func TestPathReasons(t *testing.T) {
	cases := []struct {
		name  string
		paths []graph.Path
		want  Reason
	}{
		{"none", nil, Reason{Kind: KindEverything, What: noListYet}},
		{"a correlator couldn't tell", []graph.Path{{Changed: "plugin:react", Steps: []graph.Step{{To: "plugin:react", Kind: graph.KindEverything}}}},
			Reason{Kind: KindEverything, What: "react couldn't tell what the recorded slices touched in one of the trees (its warning says why), so every slice it binds runs"}},
		{"an sbom couldn't tell about a file", []graph.Path{{Changed: "file:web/a.ts", Steps: []graph.Step{{From: "plugin:ts", To: "file:web/a.ts", Kind: graph.KindUntold}}}},
			Reason{Kind: KindEverything, What: "ts couldn't tell about web/a.ts, which changed, and the slice reaches what it lists"}},
		{"a unit that can't be told", []graph.Path{{Listed: "ts:a#init", Changed: "ts:a#fn:f", Why: "ts: links to dep:npm:@/shared, which no plugin describes", Steps: []graph.Step{{From: "ts:a#init", To: "ts:a#fn:f", Kind: graph.KindCannotTell}}}},
			Reason{Kind: KindEverything, What: "it reaches ts:a#init, which can't be told (ts: links to dep:npm:@/shared, which no plugin describes), in the group of ts:a#fn:f, which changed"}},
		{"a unit no sbom lists", []graph.Path{{Listed: "dep:npm:react", Changed: "dep:npm:react", Steps: []graph.Step{{To: "dep:npm:react", Kind: graph.KindUnlisted}}}},
			Reason{Kind: KindEverything, What: "it touched dep:npm:react, which no configured sbom lists: configure an sbom that lists npm units, or every change runs it"}},
		{"ran it", []graph.Path{{Listed: "go:a#F", Changed: "go:a#F"}}, Reason{Kind: KindExecuted, What: "go:a#F"}},
		{"reaches it", []graph.Path{{Listed: "go:a#F", Changed: "go:c#T", Steps: []graph.Step{
			{From: "go:a#F", To: "go:b#G", Kind: "uses"},
			{From: "go:c#T", To: "go:b#G", Kind: "uses", Back: true},
		}}}, Reason{Kind: KindDepends, What: "go:c#T", Through: []string{"go:b#G", "go:a#F"}}},
		{"a plugin", []graph.Path{{Listed: "go:a#F", Changed: "plugin:go", Steps: []graph.Step{
			{From: "go:a#F", To: "plugin:go", Kind: graph.KindPin},
		}}}, Reason{Kind: KindPlugin, What: "go"}},
		{"unbound names", []graph.Path{{Listed: "go:a#F", Changed: "group:x", Steps: []graph.Step{
			{From: "go:a#F", To: "group:x", Kind: graph.KindNames},
		}}}, Reason{Kind: KindDepends, What: "the names nothing binds in x"}},
	}

	hops := pathReasons([]graph.Path{{Listed: "api:createNote", Changed: "go:api#func:validate",
		Nodes: map[string]graph.Node{"api:createNote": {Label: "POST /notes", File: "api/notes.yaml", Plugin: "openapi"}, "go:api#func:validate": {Label: "api.validate"}},
		Steps: []graph.Step{{From: "api:createNote", To: "go:api#func:validate", Kind: "uses", Plugin: "api-uses"}}}})[0].Hops
	if want := []Hop{
		{To: "api:createNote", Kind: "listed", Plugin: "openapi", Label: "POST /notes", File: "api/notes.yaml"},
		{From: "api:createNote", To: "go:api#func:validate", Kind: "uses", Plugin: "api-uses", Label: "api.validate"},
	}; !slices.Equal(hops, want) {
		t.Errorf("hops %+v, want %+v", hops, want)
	}

	for _, c := range cases {
		got := pathReasons(c.paths)
		if len(got) != 1 || got[0].Kind != c.want.Kind || got[0].What != c.want.What || !slices.Equal(got[0].Through, c.want.Through) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestWalkedSuitesExpectNoSelectors(t *testing.T) {
	cfg := &config.Config{Suites: map[string]config.Suite{
		"rec":   {Tests: []string{"rec/*"}, Bind: config.BindRecorded},
		"graph": {Tests: []string{"graph/*"}, Bind: config.BindGraph},
	}}
	tests := map[string][]bazel.Slice{
		"rec":   {walkSlice("rec/a_test.go"), walkSlice("rec/b_test.go")},
		"graph": {walkSlice("graph/g_test.go")},
	}
	o := Options{
		Config:  cfg,
		Prepare: func(context.Context, string, string, map[string][]bazel.Slice) (string, error) { return "", nil },
		Expect:  func(string) int { return 10 },
	}

	if got := o.expectedDeps("c", tests); got != 16 {
		t.Fatalf("selectors: %d, want 16", got)
	}

	o.Select = &graph.Selection{}
	if got := o.expectedDeps("c", tests); got != 0 {
		t.Fatalf("walked: %d, want none: no deps repository is written", got)
	}
}

func TestWalkedInputsAreExplained(t *testing.T) {
	d := &fakeDiffer{causes: map[string][]RootCause{"//data:a": {{Label: "//data:a.json", Path: []string{"//data:a.json", "//data:a"}}}}}

	o, done := withExplainBudget(Options{Differ: d, ExplainLimit: 1})
	defer done()

	b, h := &Snapshot{}, &Snapshot{}
	by := map[string][]Reason{
		bazel.SelectorLabel("rec/a_test.go"): {{Kind: KindTarget, What: "//data:a"}},
		bazel.SelectorLabel("rec/b_test.go"): {{Kind: KindTarget, What: "//data:a"}},
		bazel.SelectorLabel("rec/c_test.go"): {{Kind: KindTarget, What: "//data:c"}},
	}

	got := o.walkedReasons(t.Context(), b, h, []bazel.Slice{walkSlice("rec/a_test.go"), walkSlice("rec/b_test.go"), walkSlice("rec/c_test.go")}, by)

	for _, name := range []string{"rec/a_test.go", "rec/b_test.go"} {
		a := got[bazel.SelectorLabel(name)]
		if len(a) != 1 || a[0].What != "//data:a.json" || !slices.Equal(a[0].Through, []string{"//data:a.json", "//data:a"}) {
			t.Fatalf("%s explained: %+v", name, a)
		}
	}

	if rs := got[bazel.SelectorLabel("rec/c_test.go")]; len(rs) != 1 || rs[0].What != "//data:c" || !slices.Equal(rs[0].Through, []string{unexplained}) {
		t.Fatalf("past the budget: %+v", rs)
	}

	if o.explained.skipped != 1 {
		t.Fatalf("skipped %d", o.explained.skipped)
	}
}

var errNever = errors.New("never recorded")

func TestWalkedInputsNeverRecordedFailThePlan(t *testing.T) {
	o := Options{
		Config: &config.Config{Suites: map[string]config.Suite{"rec": {Tests: []string{"rec/*"}, Bind: config.BindRecorded}}},
		Select: &graph.Selection{},
		Inputs: func(commit, _, _ string) ([]string, error) {
			if commit == commitBase {
				return nil, errNever
			}

			return []string{"//data:a"}, nil
		},
	}

	sl := []bazel.Slice{walkSlice("rec/a_test.go")}

	_, err := o.join(t.Context(), "rec", &Snapshot{Commit: commitBase, Slices: map[string][]bazel.Slice{"rec": sl}},
		&Snapshot{Commit: commitHead, Slices: map[string][]bazel.Slice{"rec": sl}}, nil, nil, hashedLabels(""))
	if !errors.Is(err, errNever) || !strings.Contains(err.Error(), "rec/a_test.go at "+commitBase) {
		t.Fatalf("err %v", err)
	}
}

func TestShortLabelsNameTheirPackagesTarget(t *testing.T) {
	got := mainLabelList([]string{"//web", "@//api/money", "@@//a/b:c", "//:code", "@other//x", "//web:web"})
	if want := []string{"//:code", "//a/b:c", "//api/money:money", "//web:web", "@other//x:x"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}

	a := "rec/a_test.go"
	in := map[string]map[string][]string{commitBase: {a: {"//web"}}, commitHead: {a: {"//web"}}}

	c := walkCase{inputs: in, impacted: []string{"//web:web"}, hashed: []string{"//web:web"}}
	if sps, _ := c.plan(t, true); names(suitePlan(t, sps, "rec")) != a {
		t.Fatalf("//web impacted: %q", names(suitePlan(t, sps, "rec")))
	}

	c.impacted = nil
	if sps, warnings := c.plan(t, true); names(suitePlan(t, sps, "rec")) != "" || len(warnings) > 0 {
		t.Fatalf("//web unchanged: %q, warnings %v", names(suitePlan(t, sps, "rec")), warnings)
	}
}

func TestInputReasonsSayWhatHappened(t *testing.T) {
	d := &fakeDiffer{causes: map[string][]RootCause{"//data:a": {{Label: "//data:a.json", Path: []string{"//data:a.json", "//data:a"}}}}}

	o := Options{Differ: d}

	sel := bazel.SelectorLabel("rec/a_test.go")
	by := map[string][]Reason{sel: {
		{Kind: KindInputAdded, What: "//data:b"}, {Kind: KindTarget, What: "//data:a"},
		{Kind: KindInputRemoved, What: "//data:c"}, {Kind: KindInputUnknown, What: "@ext//x:y"},
	}}

	got := o.walkedReasons(t.Context(), &Snapshot{}, &Snapshot{}, []bazel.Slice{walkSlice("rec/a_test.go")}, by)[sel]

	want := []Reason{
		{Kind: KindInputAdded, What: "//data:b"}, {Kind: KindTarget, What: "//data:a.json", Through: []string{"//data:a.json", "//data:a"}},
		{Kind: KindInputRemoved, What: "//data:c"}, {Kind: KindInputUnknown, What: "@ext//x:y"},
	}

	if len(got) != len(want) {
		t.Fatalf("%+v, want %+v", got, want)
	}

	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].What != want[i].What || !slices.Equal(got[i].Through, want[i].Through) {
			t.Fatalf("%+v, want %+v", got, want)
		}
	}
}

func TestAnEntrysPathIsTheEntrysReason(t *testing.T) {
	got := pathReasons([]graph.Path{{Listed: "t:a", Changed: "t:a", Entry: "texts"}})
	if len(got) != 1 || got[0].Kind != KindPlugin || got[0].What != "texts" {
		t.Errorf("%+v", got)
	}
}
