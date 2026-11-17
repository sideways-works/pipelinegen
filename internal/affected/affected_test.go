package affected

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

type fakeDiffer struct {
	t        *testing.T
	repo     gitx.Repo
	hashed   []string
	impacted map[string][]string
	causes   map[string][]RootCause
	failOn   string
	failDiff bool
}

var errFake = errors.New("fake bazel-diff")

func (f *fakeDiffer) Hashes(ctx context.Context, _ *bazel.Client, dir, out, edges string) error {
	head, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("rev-parse: %w", err)
	}

	commit := strings.TrimSpace(string(head))
	if commit == f.failOn {
		return fmt.Errorf("%w: broken BUILD file", errFake)
	}

	f.hashed = append(f.hashed, commit)

	if err := os.WriteFile(out, []byte(commit), 0o600); err != nil {
		return fmt.Errorf("write hashes: %w", err)
	}

	if err := os.WriteFile(edges, []byte("{}"), 0o600); err != nil {
		return fmt.Errorf("write edges: %w", err)
	}

	return nil
}

func (f *fakeDiffer) Explain(_ context.Context, _, _, _, target string) ([]RootCause, error) {
	return f.causes[target], nil
}

func (f *fakeDiffer) Impacted(_ context.Context, _ *bazel.Client, base, head string) ([]string, error) {
	if f.failDiff {
		return nil, fmt.Errorf("%w: crashed", errFake)
	}

	return f.impacted[readFile(f.t, base)+".."+readFile(f.t, head)], nil
}

type setup struct {
	r         *testrepo.Repo
	cfg       *config.Config
	differ    *fakeDiffer
	cache     string
	bz        map[string]*bazel.Client
	allReason string
}

func newSetup(t *testing.T) *setup {
	t.Helper()
	testrepo.NeedBazel(t)

	r := testrepo.New(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	s := &setup{r: r, cfg: cfg, cache: t.TempDir(), bz: map[string]*bazel.Client{}}
	s.differ = &fakeDiffer{t: t, repo: gitx.Repo{Dir: r.Dir}, impacted: map[string][]string{}, causes: map[string][]RootCause{}}

	return s
}

func (s *setup) plan(t *testing.T, base, head string, all bool) *Plan {
	t.Helper()

	p, err := Compute(t.Context(), Options{
		Config: s.cfg, Repo: gitx.Repo{Dir: s.r.Dir}, Cache: s.cache,
		Base: base, Head: head, All: all, AllReason: s.allReason, Differ: s.differ,
		Bazel: func(dir string) *bazel.Client {
			if c, ok := s.bz[dir]; ok {
				return c
			}

			c := testrepo.Bazel(t, dir)
			s.bz[dir] = c

			return c
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func selected(p *Plan, suite string) string {
	for _, sp := range p.Suites {
		if sp.Suite == suite {
			labels := []string{}
			for _, sl := range sp.Selected {
				labels = append(labels, sl.Label)
			}

			return strings.Join(labels, ",")
		}
	}

	return "?"
}

func TestComputeSelects(t *testing.T) {
	s := newSetup(t)
	base := s.r.Git("rev-parse", "HEAD")

	s.r.Write("api/orders/orders.go", "package orders // changed\n")
	head := s.r.Commit("orders")

	s.differ.impacted[base+".."+head] = []string{"//api/orders:orders", "//api/orders:orders.go", "//e2e:orders"}
	s.differ.causes["//e2e:orders"] = []RootCause{{Label: "//api/orders:orders", Path: []string{"//api/orders:orders", "//e2e:orders"}}}

	p := s.plan(t, base, head, false)
	if p.All || p.Base != base || p.Head != head {
		t.Fatalf("plan: %+v", p)
	}

	if got := selected(p, "e2e"); got != "//e2e:orders" {
		t.Fatalf("e2e selected %q", got)
	}

	if got := selected(p, "backend"); got != "" {
		t.Fatalf("backend selected %q", got)
	}

	for _, sp := range p.Suites {
		if sp.Suite == "e2e" && strings.Join(sp.Untouched, ",") != "//e2e:checkout" {
			t.Fatalf("untouched: %v", sp.Untouched)
		}
	}

	for _, sp := range p.Suites {
		if sp.Suite != "e2e" {
			continue
		}

		rs := sp.Reasons["//e2e:orders"]
		if len(rs) != 1 || rs[0].Kind != KindTarget || rs[0].What != "//api/orders:orders.go" || len(rs[0].Through) != 2 || rs[0].Through[0] != "//api/orders:orders" {
			t.Fatalf("reasons %+v", sp.Reasons)
		}
	}

	s.plan(t, base, head, false)

	if len(s.differ.hashed) != 2 {
		t.Fatalf("hashed %v", s.differ.hashed)
	}
}

func TestComputeEverything(t *testing.T) {
	s := newSetup(t)
	first := s.r.Git("rev-parse", "HEAD")

	s.r.Write("ci/e2e-slice.yml", "platform: linux # changed\n")
	ciChange := s.r.Commit("ci")

	s.r.Write("stray.txt", "no target owns me\n")
	stray := s.r.Commit("stray")

	s.r.Git("checkout", "-q", "-b", "side", first)
	s.r.Write("web/app.ts", "side\n")
	side := s.r.Commit("side")

	cases := []struct {
		name, base, head string
		all              bool
		reason           string
		allReason        string
	}{
		{"nightly", "", ciChange, true, "every slice runs", ""},
		{"uncovered", first, ciChange, true, "files no slice covers: stray.txt", "files no slice covers: stray.txt"},
		{"no base", "", ciChange, false, "nothing to compare with", ""},
		{"unknown base", "0123456789abcdef0123456789abcdef01234567", ciChange, false, "is unknown", ""},
		{"not an ancestor", side, ciChange, false, "no ancestor", ""},
		{"everything_on", first, ciChange, false, "ci/e2e-slice.yml changed (everything_on ci/**)", ""},
		{"no owner", ciChange, stray, false, "stray.txt changed and no Bazel target owns it", ""},
	}
	for _, c := range cases {
		s.allReason = c.allReason
		p := s.plan(t, c.base, c.head, c.all)

		if !p.All || !strings.Contains(p.Reason, c.reason) {
			t.Errorf("%s: all=%v reason %q", c.name, p.All, p.Reason)

			continue
		}

		if got := selected(p, "e2e"); got != "//e2e:checkout,//e2e:orders" {
			t.Errorf("%s: e2e selected %q", c.name, got)
		}

		if got := selected(p, "backend"); got != "//backendtests:money_test" {
			t.Errorf("%s: backend selected %q", c.name, got)
		}
	}
}

func TestComputeFailuresSelectEverything(t *testing.T) {
	s := newSetup(t)
	base := s.r.Git("rev-parse", "HEAD")

	s.r.Write("web/app.ts", "changed\n")
	head := s.r.Commit("web")

	s.differ.failOn = base
	if p := s.plan(t, base, head, false); !p.All || !strings.Contains(p.Reason, "could not be hashed") {
		t.Fatalf("base fails: %+v", p)
	}

	s.differ.failOn = ""
	s.differ.failDiff = true

	if p := s.plan(t, base, head, false); !p.All || !strings.Contains(p.Reason, "bazel-diff failed") {
		t.Fatalf("diff fails: %+v", p)
	}

	s.differ.failDiff = false

	s.r.Write("web/BUILD.bazel", strings.Replace(readFile(t, s.r.Dir+"/web/BUILD.bazel"), `"app.ts"`, `"app.ts",`, 1))
	build := s.r.Commit("build")

	s.differ.impacted[head+".."+build] = []string{"//web:web", "//e2e:checkout", "//e2e:orders"}
	if p := s.plan(t, head, build, false); p.All {
		t.Fatalf("BUILD change selected everything: %s", p.Reason)
	}

	if _, err := Compute(t.Context(), Options{Config: s.cfg, Repo: gitx.Repo{Dir: s.r.Dir}, Cache: s.cache, Head: "nope", Differ: s.differ}); !errors.Is(err, ErrHead) {
		t.Fatalf("unknown head: %v", err)
	}
}

func TestSelectSuiteWholeSuiteChecks(t *testing.T) {
	suite := config.Suite{WholeSuite: []config.WholeSuiteCheck{
		{Name: "reconcile", AlsoOn: []string{"//ledger"}},
		{Name: "coverage"},
	}}
	all := []bazel.Slice{{Label: "//a"}, {Label: "//b"}}

	sp := selectSuite("s", suite, all, map[string]bool{"//a": true}, false)
	if sp.All || len(sp.WholeSuite) != 0 {
		t.Fatalf("partial: %+v", sp)
	}

	sp = selectSuite("s", suite, all, map[string]bool{"//a": true, "//ledger": true}, false)
	if strings.Join(sp.WholeSuite, ",") != "reconcile" {
		t.Fatalf("also_on: %+v", sp.WholeSuite)
	}

	sp = selectSuite("s", suite, all, map[string]bool{"//a": true, "//b": true}, false)
	if !sp.All || strings.Join(sp.WholeSuite, ",") != "reconcile,coverage" {
		t.Fatalf("every slice: %+v", sp)
	}

	if sp := selectSuite("s", suite, nil, map[string]bool{}, false); sp.All {
		t.Fatal("a suite without slices counts as all")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestSelectSuiteSetupChange(t *testing.T) {
	suite := config.Suite{WholeSuite: []config.WholeSuiteCheck{{Name: "coverage"}}}
	all := []bazel.Slice{{Label: "//a"}, {Label: "//b"}}
	setup := bazel.SelectorLabel(recording.SetupTarget("s"))

	sp := selectSuite("s", suite, all, map[string]bool{setup: true}, false)
	if !sp.All || len(sp.Selected) != 2 || len(sp.Untouched) != 0 || !strings.Contains(sp.Reason, "setup") {
		t.Fatalf("setup changed: %+v", sp)
	}

	if strings.Join(sp.WholeSuite, ",") != "coverage" {
		t.Fatalf("whole-suite checks: %+v", sp.WholeSuite)
	}

	if sp := selectSuite("other", suite, all, map[string]bool{setup: true}, false); sp.All || len(sp.Selected) != 0 {
		t.Fatalf("other suite: %+v", sp)
	}
}

func TestSelectAtTheWidth(t *testing.T) {
	file := bazel.Slice{Name: "x_test.go", Label: bazel.SelectorLabel("x_test.go")}
	tagged := bazel.Slice{Name: "//t", Label: "//t"}
	all := []bazel.Slice{file, tagged}
	setup := bazel.SelectorLabel(recording.SetupTarget("s"))

	cases := []struct {
		width    string
		impacted []string
		want     string
		all      bool
	}{
		{config.FollowExecuted, []string{file.Label + ".dependents"}, "", false},
		{config.FollowDependents, []string{file.Label + ".dependents"}, file.Label, false},
		{config.FollowDependents, []string{file.Label}, "", false},
		{config.FollowDependents, []string{"//t"}, "//t", false},
		{config.FollowDependents, []string{setup + ".dependents"}, file.Label + ",//t", true},
		{config.FollowDependents, []string{setup}, "", false},
	}

	for _, c := range cases {
		impacted := map[string]bool{}
		for _, l := range c.impacted {
			impacted[l] = true
		}

		sp := selectSuite("s", config.Suite{}, all, atWidth(impacted, "s", c.width, all), false)

		var got []string
		for _, sl := range sp.Selected {
			got = append(got, sl.Label)
		}

		if strings.Join(got, ",") != c.want || sp.All != c.all {
			t.Errorf("%s %v: selected %v (all %v), want %q", c.width, c.impacted, got, sp.All, c.want)
		}
	}
}

func TestAnUntestedFileNoTargetOwnsSelectsNothing(t *testing.T) {
	s := newSetup(t)
	s.cfg.Untested = []config.Untested{{Paths: []string{"docs/assets/**", ".github/**", "vendored/**"}, Reason: "read by people only"}}

	s.r.Write(".bazelignore", readFile(t, s.r.Dir+"/.bazelignore")+"vendored\n")
	base := s.r.Commit("ignore vendored")

	for _, f := range []string{"docs/assets/social.jpg", ".github/workflows/ci.yml", "vendored/BUILD.bazel"} {
		s.r.Write(f, "changed\n")
		head := s.r.Commit(f)

		p := s.plan(t, base, head, false)
		if p.All {
			t.Errorf("%s selected everything: %s", f, p.Reason)
		}

		if want := f + ": untested by declaration: read by people only"; !slices.Contains(p.Warnings, want) {
			t.Errorf("%s: warnings %q, want %q", f, p.Warnings, want)
		}

		base = head
	}

	s.r.Write("stray.txt", "no target owns me\n")
	stray := s.r.Commit("stray")

	if p := s.plan(t, base, stray, false); !p.All || !strings.Contains(p.Reason, "stray.txt changed and no Bazel target owns it") {
		t.Errorf("an unowned file nothing declares: all=%v %q", p.All, p.Reason)
	}
}

func TestAPlanOfEverythingForAKnownReasonNamesItsBase(t *testing.T) {
	s := newSetup(t)
	base := s.r.Git("rev-parse", "HEAD")

	s.r.Write("extra.txt", "x\n")
	head := s.r.Commit("extra")

	s.allReason = "MODULE.bazel changed (everything_on MODULE.bazel)"

	p := s.plan(t, base, head, true)
	if !p.All || p.Base != base {
		t.Fatalf("all %v, base %q, want %q", p.All, p.Base, base)
	}
}
