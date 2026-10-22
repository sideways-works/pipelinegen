package report

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func sampleRun() Run {
	return Run{
		Name: "pr", Commit: "0123456789abcdef0123", URL: "https://ci.example/builds/1",
		Suites: []Suite{
			{
				Name: "e2e",
				Slices: []Slice{
					{Label: "//e2e:orders", State: Passed, URL: "https://ci.example/builds/3"},
					{Label: "//e2e:checkout", State: Failed, URL: "https://ci.example/builds/2", Failures: []Failure{
						{Test: "checkout pays with a card", Tail: "expected 200\ngot 500"},
					}},
				},
				Untouched: []string{"//e2e:search", "//e2e:account"},
			},
			{Name: "backend", Slices: []Slice{{Label: "//api:money_test", State: Running}}},
		},
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		suite       Suite
		state, desc string
	}{
		{Suite{Slices: []Slice{{State: Passed}, {State: Passed}}, Untouched: []string{"a"}}, "success", "2 of 3 slices ran, all passed"},
		{Suite{Slices: []Slice{{State: Passed}, {State: Running}, {State: Pending}}, Untouched: []string{"a"}}, "pending", "1 of 3 slices done, 1 untouched"},
		{Suite{Slices: []Slice{{State: Failed}, {State: Running}}}, "pending", "1 of 2 slices done, 1 failed so far"},
		{Suite{Slices: []Slice{{State: Failed, Failures: []Failure{{Test: "a"}, {Test: "b"}}}, {State: Passed}}}, "failure", "2 of 2 slices ran, 1 failed (2 tests)"},
		{Suite{Slices: []Slice{
			{State: Failed, Failures: []Failure{{Test: "a"}}},
			{State: Failed, Failures: []Failure{{Test: "a"}, {Test: "(3 more)"}}},
		}}, "failure", "2 of 2 slices ran, 2 failed (4 tests)"},
		{Suite{Slices: []Slice{{State: Errored}, {State: Failed}}}, "error", "2 of 2 slices ran, 1 errored, 1 failed"},
		{Suite{Slices: []Slice{{State: NotRun}, {State: NotRun}, {State: Failed}}, Untouched: []string{"c"}}, "error", "1 of 4 slices ran, 2 not run (a batch's setup failed), 1 failed"},
		{Suite{Untouched: []string{"a", "b"}}, "success", "nothing selected, 2 untouched"},
	}
	for _, c := range cases {
		state, desc := c.suite.Status()
		if state != c.state || desc != c.desc {
			t.Errorf("%+v: %s %q", c.suite, state, desc)
		}
	}
}

func TestReadResult(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return p
	}

	if s, f := ReadResult(write("ok.json", `{"passed": true}`)); s != Passed || f != nil {
		t.Fatalf("passed: %s %v", s, f)
	}

	items := make([]string, 0, 12)
	for i := range 12 {
		items = append(items, fmt.Sprintf(`{"test": "t%d", "tail": %q}`, i, strings.Repeat("line\n", 40)))
	}

	s, f := ReadResult(write("bad.json", `{"passed": false, "failures": [`+strings.Join(items, ",")+"]}"))
	if s != Failed || len(f) != 11 || f[10].Test != "(2 more)" || strings.Count(f[0].Tail, "\n") != 14 {
		t.Fatalf("failed: %s %d %+v", s, len(f), f[len(f)-1])
	}

	if s, _ := ReadResult(write("odd.json", `{"passed": true, "failures": [{"test": "x"}]}`)); s != Failed {
		t.Fatalf("odd: %s", s)
	}

	if s, _ := ReadResult(filepath.Join(dir, "missing.json")); s != Errored {
		t.Fatalf("missing: %s", s)
	}

	if s, _ := ReadResult(write("junk.json", "nope")); s != Errored {
		t.Fatalf("junk: %s", s)
	}
}

func TestCommentList(t *testing.T) {
	body := Comment(sampleRun(), 42, List)

	for _, want := range []string{
		"<!-- pipelinegen:pr:42 -->\n",
		"**pr** run of `0123456789ab` ([plan](https://ci.example/builds/1))",
		"- **backend**: 0 of 1 slices done",
		"- **e2e**: 2 of 4 slices ran, 1 failed (1 test)",
		"- **failed** [`e2e:checkout`](https://ci.example/builds/2)\n- **passed** [`e2e:orders`]",
		"- untouched: 2 slices",
		"#### Failures",
		"  - `checkout pays with a card`",
		"    got 500",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}

	if strings.Contains(body, "```mermaid") {
		t.Error("list has a chart")
	}
}

func TestCommentMermaid(t *testing.T) {
	body := Comment(sampleRun(), 42, Mermaid)

	for _, want := range []string{
		"```mermaid\nflowchart LR\n",
		`s1_0["e2e:checkout"]:::failed`,
		`s1_u["2 untouched"]:::untouched`,
		`plan --> s0["backend: 0 of 1 slices done"]`,
		"#### Failures",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chart lacks %q:\n%s", want, body)
		}
	}

	big := sampleRun()
	for i := range mermaidMaxEdges {
		big.Suites[0].Slices = append(big.Suites[0].Slices, Slice{Label: fmt.Sprintf("//e2e:s%03d", i), Group: fmt.Sprintf("g%03d", i), State: Passed})
	}

	if body := Comment(big, 42, Mermaid); strings.Contains(body, "```mermaid") || !strings.Contains(body, "<b>g399</b>: 1 passed") {
		t.Fatal("no fallback to the list")
	}
}

func TestCommentStaysUnderGitHubsLimit(t *testing.T) {
	r := sampleRun()
	for i := range 3000 {
		r.Suites[0].Slices = append(r.Suites[0].Slices, Slice{
			Label: fmt.Sprintf("//e2e:long_slice_name_%04d", i), State: Failed,
			Failures: []Failure{{Test: "a test", Tail: strings.Repeat("x", 1000)}},
		})
	}

	body := Comment(r, 1, List)
	if len(body) > maxCommentBytes || !strings.HasPrefix(body, Marker("pr", 1)) || !strings.HasSuffix(body, "size limit)\n") {
		t.Fatalf("len %d", len(body))
	}
}

func TestMermaidEscapes(t *testing.T) {
	r := Run{Name: `a"b`, Commit: "c", Suites: []Suite{{Name: "<s>", Slices: []Slice{{Label: `//x:"y"`, State: Passed}}}}}

	chart, ok := mermaid(r.Sorted())
	if !ok || strings.Contains(chart, `"y"`) || !strings.Contains(chart, "#quot;y#quot;") || !strings.Contains(chart, "#lt;s#gt;") {
		t.Fatal(chart)
	}
}

func reasonRun() Run {
	r := sampleRun()
	r.Reasoning = true
	r.Suites[0].Removed = []string{"//e2e:gone"}
	r.Suites[0].Slices[1].Reasons = []Reason{
		{Kind: "test-changed", What: "e2e/checkout.spec.ts"},
		{Kind: "depends", What: "orders#func:Create", Through: []string{"a", "b", "c", "d", "e", "f"}},
		{Kind: "opens", What: "apps/web/main.ts", Through: []string{"//apps/web:web"}},
		{Kind: "executed", What: "orders#func:Total"},
	}

	return r
}

func TestReasonsAndRemoved(t *testing.T) {
	body := Comment(reasonRun(), 42, List)

	for _, want := range []string{
		"- **failed** [`e2e:checkout`](https://ci.example/builds/2)\n  - test changed: `e2e/checkout.spec.ts`\n",
		"  - reaches `orders#func:Create`, which changed, through `a` → `b` → `c` → (2 more) → `f`\n",
		"  - `apps/web/main.ts` changed in an app it opens, through `//apps/web:web`\n",
		"  - and 1 more\n",
		"- removed: `e2e:gone`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}

	chart := Comment(reasonRun(), 42, Mermaid)
	for _, want := range []string{
		"#### Why these slices run\n\n- `e2e e2e:checkout`: test changed: `e2e/checkout.spec.ts`; reaches",
		"Removed from **e2e**: `e2e:gone`",
	} {
		if !strings.Contains(chart, want) {
			t.Errorf("chart lacks %q:\n%s", want, chart)
		}
	}

	off := reasonRun()
	off.Reasoning = false

	if body := Comment(off, 42, List); strings.Contains(body, "test changed") || !strings.Contains(body, "- removed: `e2e:gone`") {
		t.Fatalf("reasoning off:\n%s", body)
	}
}

func TestReasonsGoBeforeSlicesAndFailures(t *testing.T) {
	r := reasonRun()
	for i := range 400 {
		r.Suites[0].Slices = append(r.Suites[0].Slices, Slice{
			Label: fmt.Sprintf("//e2e:slice_%03d", i), State: Passed,
			Reasons: []Reason{{Kind: "executed", What: strings.Repeat("x", 150)}},
		})
	}

	body := Comment(r, 1, List)
	if len(body) > maxCommentBytes || strings.Contains(body, "executed `xxx") || !strings.Contains(body, "e2e:slice_399") || !strings.Contains(body, "got 500") {
		t.Fatalf("len %d, reasons kept or slices or tails lost", len(body))
	}
}

func TestManySlicesFoldIntoGroups(t *testing.T) {
	r := Run{Name: "pr", Commit: "c", Suites: []Suite{{Name: "e2e", Untouched: []string{"x"}}}}

	add := func(name, group string, st State) {
		sl := Slice{Name: name, Group: group, State: st, URL: "https://ci/jobs/e2e"}
		if st == Failed {
			sl.Failures = []Failure{{Test: "pays"}}
		}

		r.Suites[0].Slices = append(r.Suites[0].Slices, sl)
	}

	for i := range 6 {
		add(fmt.Sprintf("e2e/checkout-%d.spec.ts", i), "checkout", Passed)
	}

	add("e2e/checkout-9.spec.ts", "checkout", Failed)
	add("e2e/search-1.spec.ts", "search", Running)
	add("e2e/search-2.spec.ts", "search", Pending)
	add("e2e/search-3.spec.ts", "search", Passed)

	body := Comment(r, 1, List)
	for _, want := range []string{
		"#### e2e\n\n- **failed** [`e2e/checkout-9.spec.ts`](https://ci/jobs/e2e)\n- <details><summary><b>checkout</b>: 6 passed</summary>",
		"- <details><summary><b>search</b>: 1 running, 1 pending, 1 passed</summary>\n\n  - **running** [`e2e/search-1.spec.ts`]",
		"- untouched: 1 slices",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}

	chart := Comment(r, 1, Mermaid)
	for _, want := range []string{
		`s0_g0["checkout: 1 failed, 6 passed"]:::failed`,
		`s0_g1["search: 1 running, 1 pending, 1 passed"]:::running`,
	} {
		if !strings.Contains(chart, want) {
			t.Errorf("chart lacks %q:\n%s", want, chart)
		}
	}

	if strings.Contains(chart, "checkout-1.spec.ts") {
		t.Error("the chart draws single files")
	}
}

func TestGroupOf(t *testing.T) {
	re := regexp.MustCompile(`^e2e/([a-z]+)/`)

	for _, c := range [][3]string{
		{"", "e2e/checkout-pays.spec.ts", "checkout"},
		{"", "backend/api/orders_test.go", "orders"},
		{"", "lint.go", "lint"},
		{"re", "e2e/console/search.spec.ts", "console"},
		{"re", "e2e/top.spec.ts", "top"},
	} {
		var p *regexp.Regexp
		if c[0] != "" {
			p = re
		}

		if got := groupOf(p, c[1]); got != c[2] {
			t.Errorf("%s: %q, want %q", c[1], got, c[2])
		}
	}
}

func TestRefPathsHoldAnyFileName(t *testing.T) {
	names := []string{
		"e2e/console/checkout/pays.spec.ts", "a b/c~d.ts", ".hidden/x..y.lock", "dir/.env.spec.ts", "x.", "100%.ts", "lint.go",
		"a/=", "a/%3D", "e2e/a", "e2e/a/b.ts",
	}

	seen := map[string]string{}

	for _, name := range names {
		p := RefPath(name)
		if strings.ContainsAny(p, " ~^:?*[\\") || strings.Contains(p, "..") || strings.HasSuffix(p, ".lock") || strings.Contains(p, "/.") || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") {
			t.Errorf("%q: %q can't be a ref", name, p)
		}

		if back, err := NameFromRef(p); err != nil || back != name {
			t.Errorf("%q: back %q %v", name, back, err)
		}

		if other, ok := seen[p]; ok {
			t.Errorf("%q and %q both encode to %q", name, other, p)
		}

		seen[p] = name
	}

	if p := RefPart("release/1.2"); strings.Contains(p, "/") {
		t.Fatalf("%q", p)
	}
}

func TestEveryRefIsValidAndNoneIsAnothersDirectory(t *testing.T) {
	refs := map[string]bool{}

	for _, id := range []ID{
		{Run: "main", Change: "release/1.2", Commit: "0123456789abcdef"},
		{Run: "main", Change: "feat/x", Commit: "0123456789abcdef"},
		{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef"},
	} {
		if err := id.Check(); err != nil {
			t.Fatal(err)
		}

		refs[id.PlanRef()] = true
		refs[id.EndRef("e2e")] = true
		refs[AlsoToRef(id.Run, id.Change)] = true

		for _, slice := range []string{"e2e/a", "e2e/a/b.ts", "dir/.env.spec.ts", "x.lock", "a b/c.ts", "lint.go"} {
			ref := id.StateRef("e2e", slice)
			refs[ref] = true

			suite, back, ok, err := id.sliceOf(ref)
			if err != nil || !ok || suite != "e2e" || back != slice {
				t.Errorf("%s: %s %s %v %v", ref, suite, back, ok, err)
			}
		}
	}

	for ref := range refs {
		if out, err := exec.CommandContext(t.Context(), "git", "check-ref-format", ref).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", ref, err, out)
		}

		for other := range refs {
			if strings.HasPrefix(other, ref+"/") {
				t.Errorf("%s is a directory of %s", ref, other)
			}
		}
	}
}

func TestInputReasonsRead(t *testing.T) {
	for _, c := range []struct {
		r    Reason
		want string
	}{
		{Reason{Kind: "input-added", What: "//data:b"}, "new input `//data:b`"},
		{Reason{Kind: "input-removed", What: "//data:c"}, "input `//data:c` gone"},
		{Reason{Kind: "input-unknown", What: "@ext//x:y"}, "input `@ext//x:y` not hashed, so it can't be told whether it changed"},
	} {
		if got := reasonText(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.r.Kind, got, c.want)
		}
	}
}

func causeRun(reasoning bool) Run {
	shared := Reason{Kind: "target", What: "//runner:reporter.js", Through: []string{"//runner:runner"}}
	s := Suite{
		Name: "browser", Reason: "the change reaches every slice",
		Causes: []Cause{{Kind: "target", What: "//runner:reporter.js", Through: []string{"//runner:runner"}, Slices: 12}},
	}

	for i := range 12 {
		sl := Slice{Label: fmt.Sprintf("//browser:s%02d", i), State: Passed, Reasons: []Reason{shared}}
		if i == 0 {
			sl.Reasons = append(sl.Reasons, Reason{Kind: "test-changed", What: "browser/s00.spec.ts"})
		}

		s.Slices = append(s.Slices, sl)
	}

	return Run{Name: "pr", Commit: "0123456789abcdef0123", Reasoning: reasoning, Suites: []Suite{s}}
}

func TestASharedCauseIsNamedOnce(t *testing.T) {
	const line = "- 12 slices: target `//runner:reporter.js` changed, through `//runner:runner`\n"

	for _, format := range []Format{List, Mermaid} {
		for _, reasoning := range []bool{true, false} {
			body := Comment(causeRun(reasoning), 7, format)

			if strings.Count(body, line) != 1 {
				t.Errorf("format %v, reasoning %v: the cause isn't named once:\n%s", format, reasoning, body)
			}

			if n := strings.Count(body, "reporter.js"); n != 1 {
				t.Errorf("format %v, reasoning %v: the file is named %d times:\n%s", format, reasoning, n, body)
			}

			if reasoning && !strings.Contains(body, "test changed: `browser/s00.spec.ts`") {
				t.Errorf("format %v: the slice's own reason is gone:\n%s", format, body)
			}
		}
	}

	partial := causeRun(false)
	partial.Suites[0].Reason = ""

	if body := Comment(partial, 7, List); strings.Contains(body, "Shared causes") {
		t.Errorf("reasons off, part of the suite: causes shown:\n%s", body)
	}
}

func TestConfigReasonsRead(t *testing.T) {
	for _, c := range []struct {
		r    Reason
		want string
	}{
		{Reason{Kind: "config", What: "suites.lint.steps[1] (run shell), which takes it, changed"}, "the config: suites.lint.steps[1] (run shell), which takes it, changed"},
		{Reason{Kind: "plugin", What: "shell"}, "the entry of plugin `shell` changed"},
	} {
		if got := reasonText(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.r.Kind, got, c.want)
		}
	}
}
