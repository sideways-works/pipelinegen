package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const keptPlan = `{"base": "b", "head": "h", "narrowing": 2, "suites": [{"suite": "e2e",
	"selected": [{"name": "e2e.checkout", "label": "//e2e:checkout"}, {"name": "e2e.cart", "label": "//e2e:cart"}],
	"reasons": {"//e2e:checkout": [
		{"kind": "test-changed", "what": "e2e/checkout.spec.ts"},
		{"kind": "executed", "what": "api/orders#func:Create"},
		{"kind": "depends", "what": "api/orders#func:total", "through": ["api/orders#func:total", "api/orders#func:Create"]}]},
	"untouched": ["//e2e:orders", "//e2e:refunds", "//e2e:wishlist"],
	"removed": ["//e2e:old"],
	"narrowed": [
		{"kind": "narrowed", "narrowing": "fields", "off_at": 4, "reason": "api/orders#type:Order changed in a field it never reads", "reached": 3, "dropped": ["e2e.orders"]},
		{"kind": "sampled", "narrowing": "sample", "off_at": 3, "reason": "ci/e2e.yml changed", "reached": 3, "dropped": ["e2e.wishlist"]}]}]}`

func TestWhyReadsAKeptPlan(t *testing.T) {
	root, _, store := runEnv(t)
	commit := "0123456789abcdef0123456789abcdef01234567"

	if err := report.WritePlan(t.Context(), store, report.ID{Run: "pr", Change: "pr-7", Commit: commit}, []byte(keptPlan)); err != nil {
		t.Fatal(err)
	}

	why := func(slice string, more ...string) (string, string, int) {
		return run(t, append([]string{"why", "--root", root, "--run", "pr", "--change", "pr-7", "--slice", slice}, more...)...)
	}

	for slice, want := range map[string]string{
		"e2e.checkout": "e2e e2e.checkout was selected in pr pr-7 at 0123456789ab:\n" +
			"  its test file changed: e2e/checkout.spec.ts\n" +
			"  it executed api/orders#func:Create, which changed\n" +
			"  it depends on api/orders#func:total, which changed, through api/orders#func:total → api/orders#func:Create\n",
		"e2e.cart": "e2e e2e.cart was selected in pr pr-7 at 0123456789ab:\n" +
			"  sampled: ci/e2e.yml changed; it is one of 2 the sample kept\n",
		"//e2e:orders": "e2e e2e.orders was left out in pr pr-7 at 0123456789ab:\n" +
			"  the narrowing fields left it out: api/orders#type:Order changed in a field it never reads; it would have run at narrowing level 4 or above (the plan used 2)\n",
		"e2e.wishlist": "e2e e2e.wishlist was left out in pr pr-7 at 0123456789ab:\n" +
			"  not in the sample: ci/e2e.yml changed; the nightly runs it; it would have run at narrowing level 3 or above (the plan used 2)\n",
		"e2e.refunds": "e2e e2e.refunds was left out in pr pr-7 at 0123456789ab:\n  nothing it is bound to changed\n",
		"e2e.old": "e2e e2e.old was left out in pr pr-7 at 0123456789ab:\n" +
			"  the base has it and the head doesn't: its test file or target was removed\n",
	} {
		stdout, stderr, code := why(slice)
		if code != 0 || stdout != want {
			t.Errorf("%s: %d %s\n%s\nwant\n%s", slice, code, stderr, stdout, want)
		}
	}

	stdout, _, code := why("e2e.orders", "--json", "--commit", commit)

	var answers []Answer
	if err := json.Unmarshal([]byte(stdout), &answers); err != nil || code != 0 {
		t.Fatalf("json: %d %v\n%s", code, err, stdout)
	}

	if len(answers) != 1 || answers[0].Ran || answers[0].Label != "//e2e:orders" ||
		answers[0].Reasons[0].Kind != "narrowed" || answers[0].Reasons[0].Narrowing != "fields" || answers[0].Reasons[0].OffAt != 4 {
		t.Fatalf("json: %+v", answers)
	}

	if _, stderr, code := why("e2e.nope"); code != 1 || !strings.Contains(stderr, "in no suite") {
		t.Fatalf("unknown slice: %d %s", code, stderr)
	}

	if _, stderr, code := run(t, "why", "--root", root, "--run", "pr", "--change", "pr-8", "--slice", "e2e.cart"); code != 1 || !strings.Contains(stderr, "no kept plan") {
		t.Fatalf("no plan: %d %s", code, stderr)
	}

	for _, args := range [][]string{
		{"why", "--root", root, "--run", "pr", "--change", "pr-7"},
		{"why", "--root", root, "--slice", "x"},
		{"why", "--root", root, "--run", "pr", "--slice", "x"},
		{"why", "--root", root, "--run", "pr", "--change", "pr-7", "--base", "HEAD", "--slice", "x"},
	} {
		if _, _, code := run(t, args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestWhyPlansAgain(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path

	r := testrepo.New(t)
	base := r.Git("rev-parse", "HEAD")

	r.Write("api/money/money.go", "package money // changed\n")
	r.Commit("money")

	git := t.TempDir()
	if err := os.MkdirAll(filepath.Join(git, "owner"), 0o750); err != nil {
		t.Fatal(err)
	}

	(&testrepo.Repo{Dir: git}).GitIn(t, "init", "--quiet", "--bare", filepath.Join(git, "owner", "fixture.git"))
	env := map[string]string{envGitURL: "file://" + git}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := runIn(t, r.Dir, env, "why", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-slice", "e2e/checkout.spec.ts")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if !strings.HasPrefix(stdout, "specs e2e/checkout.spec.ts was selected from "+base[:12]+" to ") {
		t.Fatalf("why said\n%s", stdout)
	}

	stdout, stderr, code = runIn(t, r.Dir, env, "why", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-slice", "e2e.orders", "-suite", "e2e")
	if code != 0 || !strings.HasPrefix(stdout, "e2e e2e.orders was selected from "+base[:12]+" to ") || !strings.Contains(stdout, ":\n  everything ran: files no slice covers: ") {
		t.Fatalf("everything: %d %s\n%s", code, stderr, stdout)
	}
}

func TestABadConfigEntryWarnsWhereverItPlans(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path

	r := testrepo.New(t)
	r.Write("pipelinegen.yaml", readText(t, filepath.Join(r.Dir, "pipelinegen.yaml"))+
		"untested:\n  - {paths: [\"**\"], reason: the fixture's other files}\n  - {paths: [.tool-versions], reason: a file it doesn't have}\n")
	r.Write("e2e/BUILD.bazel", `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["//api/money", "//web"], tags = ["slice-e2e"])

filegroup(name = "orders", srcs = ["orders.spec.ts"], data = ["//api/orders", "//web"], tags = ["slice-e2e"])

filegroup(name = "helpers", srcs = ["helpers.ts"])
`)
	r.Write("backendtests/BUILD.bazel", `filegroup(name = "money_test", srcs = ["money_test.go"], data = ["//api/money"], tags = ["manual", "slice-backend"])
`)
	base := r.Commit("config")

	r.Write("api/money/money.go", "package money // changed\n")
	r.Commit("money")

	git := t.TempDir()
	if err := os.MkdirAll(filepath.Join(git, "owner"), 0o750); err != nil {
		t.Fatal(err)
	}

	(&testrepo.Repo{Dir: git}).GitIn(t, "init", "--quiet", "--bare", filepath.Join(git, "owner", "fixture.git"))
	env := map[string]string{envGitURL: "file://" + git}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	const warning = `untested pattern ".tool-versions" matches no file`

	stdout, stderr, code := runIn(t, r.Dir, env, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-no-lists")
	if code != 0 || strings.HasPrefix(stdout, "every slice") || !strings.Contains(stdout, "backend: 1 selected") || !strings.Contains(stderr, warning) {
		t.Fatalf("affected: exit %d\n%s\n%s", code, stdout, stderr)
	}

	stdout, stderr, code = runIn(t, r.Dir, env, "why", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-slice", "e2e.orders", "-suite", "e2e")
	if code != 0 || strings.Contains(stdout, "everything ran") || !strings.Contains(stdout, warning) {
		t.Fatalf("why: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestWhyNamesTheCorrelatorsThatObserveASlice(t *testing.T) {
	p := &whyPlan{Suites: []whySuite{{
		Suite: "api", ObservedBy: []string{"go-correlator"},
		Selected: []whySelected{{Name: "api/notes_test.go", Label: "@pipelinegen_deps//slices:api/notes_test.go"}},
	}}}

	answers := explain(p, "", "api/notes_test.go")
	if len(answers) != 1 || !slices.Equal(answers[0].ObservedBy, []string{"go-correlator"}) {
		t.Fatalf("answers %+v", answers)
	}

	var b strings.Builder
	if err := writeAnswers(&b, answers, "from a to b", false, nil); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(b.String(), "  observed by: go-correlator\n") {
		t.Fatalf("why says:\n%s", b.String())
	}
}

func TestWhyReadsInputReasons(t *testing.T) {
	for _, c := range []struct {
		r    affected.Reason
		want string
	}{
		{affected.Reason{Kind: affected.KindInputAdded, What: "//data:b"}, "//data:b is a new input of it"},
		{affected.Reason{Kind: affected.KindInputRemoved, What: "//data:c"}, "//data:c is no longer an input of it"},
		{affected.Reason{Kind: affected.KindInputUnknown, What: "@ext//x:y"}, "it depends on @ext//x:y, which this commit's tree lacks or bazel-diff doesn't hash, so whether it changed can't be told"},
		{affected.Reason{Kind: affected.KindEverything, What: affected.AloneReason}, affected.AloneReason},
	} {
		if got := reasonLine(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.r.Kind, got, c.want)
		}
	}
}

func TestANewSliceRanForItsOwnReason(t *testing.T) {
	bound := affected.Reason{Kind: affected.KindEverything, What: "it is bound to every unit the plugins list: it has no list yet"}
	sp := whySuite{
		Suite:    "api",
		Unlisted: []string{"api/new_test.go"},
		Reasons:  map[string][]affected.Reason{"new": {bound}, "old": {bound}},
	}

	got := ranBecause(&whyPlan{}, sp, "api/new_test.go", "new")
	if len(got) != 1 || got[0].Text != "it has no recording yet, so it runs on any change until a run records what it touches" {
		t.Fatalf("a new slice: %+v", got)
	}

	got = ranBecause(&whyPlan{}, sp, "api/old_test.go", "old")
	if len(got) != 1 || got[0].Text != "it runs on any change: "+bound.What {
		t.Fatalf("a listed slice bound wide: %+v", got)
	}
}

func TestTheHighestLevelHasNoAbove(t *testing.T) {
	if got := levelText(2, config.MaxNarrowing); got != fmt.Sprintf("; it would have run at narrowing level %d (the plan used 2)", config.MaxNarrowing) {
		t.Fatalf("%q", got)
	}

	if got := levelText(0, 3); got != "; it would have run at narrowing level 3 or above" {
		t.Fatalf("%q", got)
	}

	if got := plural(1, "finding"); got != "1 finding" {
		t.Fatalf("%q", got)
	}
}
