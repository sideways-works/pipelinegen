package proof

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var tableExpectations = map[string]string{
	"helper-L2":                 "exact:app/price_test.go",
	"helper-L3":                 "exact:app/price_test.go",
	"helper-L5":                 "exact:app/price_test.go",
	"helper-dependents":         "exact:app/price_test.go",
	"helper-dependents-L3":      "exact:app/price_test.go",
	"config-L2":                 "sample:1:sample-bound",
	"config-L3":                 "all",
	"config-L5":                 "all",
	"config-dependents":         "sample:1:sample-bound",
	"config-dependents-L3":      "all",
	"start-L2":                  "sample:1:sample",
	"start-L3":                  "all",
	"start-L5":                  "all",
	"start-dependents":          "sample:1:sample",
	"start-dependents-L3":       "all",
	"initialiser-L2":            "sample:1:sample-bound",
	"initialiser-L3":            "all",
	"initialiser-L5":            "all",
	"initialiser-dependents":    "sample:1:sample-bound",
	"initialiser-dependents-L3": "all",
	"unused-L2":                 "none",
	"unused-L3":                 "none",
	"unused-L5":                 "none",
	"unused-dependents":         "none",
	"unused-dependents-L3":      "none",
	"bump-L2":                   "exact:report/report_test.go",
	"bump-L3":                   "exact:report/report_test.go",
	"bump-L5":                   "all",
	"bump-dependents":           "exact:report/report_test.go",
	"bump-dependents-L3":        "exact:report/report_test.go",
	"testfile-L2":               "exact:app/greet_test.go",
	"testfile-L3":               "exact:app/greet_test.go",
	"testfile-L5":               "exact:app/greet_test.go",
	"testfile-dependents":       "exact:app/greet_test.go",
	"testfile-dependents-L3":    "exact:app/greet_test.go",
	"both-L2":                   "own+sample:app/price_test.go:1:sample",
	"both-L3":                   "all",
	"both-L5":                   "all",
	"both-dependents":           "own+sample:app/price_test.go:1:sample",
	"both-dependents-L3":        "all",
	"newdecl-L2":                "none",
	"newdecl-L3":                "all",
	"newdecl-L5":                "all",
	"newdecl-dependents":        "none",
	"newdecl-dependents-L3":     "all",
	"unrun-L2":                  "none",
	"unrun-L3":                  "none",
	"unrun-L5":                  "exact:app/price_test.go",
	"unrun-dependents":          "exact:app/price_test.go",
	"unrun-dependents-L3":       "exact:app/price_test.go",
	"signature-L2":              "exact:app/price_test.go",
	"signature-L3":              "exact:app/price_test.go",
	"signature-L5":              "exact:app/price_test.go",
	"signature-dependents":      "exact:app/price_test.go",
	"signature-dependents-L3":   "exact:app/price_test.go",
	"width-L2":                  "none",
	"width-L3":                  "none",
	"width-L5":                  "exact:report/report_test.go",
	"width-dependents":          "exact:report/report_test.go",
	"width-dependents-L3":       "exact:report/report_test.go",
	"breaks-L2":                 "all",
	"breaks-L3":                 "all",
	"breaks-L5":                 "all",
	"breaks-dependents":         "all",
	"breaks-dependents-L3":      "all",
	"correntry-L2":              "named:1:plugin-entry",
	"correntry-L3":              "named:1:plugin-entry",
	"correntry-L4":              "all",
	"correntry-L5":              "all",
	"correntry-dependents":      "named:1:plugin-entry",
	"correntry-dependents-L3":   "named:1:plugin-entry",
	"sbomentry-L2":              "named:1:plugin-entry",
	"sbomentry-L3":              "named:1:plugin-entry",
	"sbomentry-L4":              "all",
	"sbomentry-L5":              "all",
	"sbomentry-dependents":      "named:1:plugin-entry",
	"sbomentry-dependents-L3":   "named:1:plugin-entry",
}

func TestEveryPlanHasItsExpectation(t *testing.T) {
	n := 0

	for _, c := range cases {
		for _, l := range levels {
			if l == "L4" && !strings.HasSuffix(c, "entry") {
				continue
			}

			label := c + "-" + l
			n++

			if got := expected(label); got != tableExpectations[label] {
				t.Errorf("%s: %q, the table says %q", label, got, tableExpectations[label])
			}
		}
	}

	if n != len(tableExpectations) {
		t.Errorf("%d plans, the table has %d", n, len(tableExpectations))
	}
}

func TestTheFixture(t *testing.T) {
	dir := t.TempDir()

	if err := WriteFixture(dir, "/out/bazel", "", Pins{"v1", "s1", "v2", "s2", "v3", "s3"}); err != nil {
		t.Fatal(err)
	}

	config := read(t, filepath.Join(dir, "pipelinegen.yaml"))
	for _, want := range []string{
		"startup_flags: [--output_user_root=/out/bazel, --max_idle_secs=120, --host_jvm_args=-Xmx1g]",
		"release: {version: v1, sha256: s1}", "release: {version: v2, sha256: s2}", "release: {version: v3, sha256: s3}",
		"narrowing: 2\n", "run_slice: ci/go-batch.yml",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("the config lacks %q:\n%s", want, config)
		}
	}

	info, err := os.Stat(filepath.Join(dir, "ci", "go-batch.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("the batch script is not executable: %v %v", info, err)
	}

	for _, f := range []string{".bazelversion", "pad/pad.s", "app/price.go", "report/report_test.go", "limits/limits.go", "unused/unused.go"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "user.name").Output()
	if err != nil || string(out) != "fixture\n" {
		t.Errorf("the fixture's git: %q %v", out, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestTheChanges(t *testing.T) {
	cases := map[string]struct{ file, want string }{
		"helper":      {"app/price.go", "return total - total/5\n"},
		"config":      {"app/config.go", "\tPort int\n\tHost string\n}\n\n// defaultPort is the port a server takes when nothing else is said.\nconst defaultPort = 8081\n"},
		"start":       {"app/start.go", `Config{Name: "app-" + name, Port: defaultPort}`},
		"initialiser": {"app/registry.go", `var registry = append(load(), "role")` + "\n"},
		"unused":      {"unused/unused.go", `return string(r) + ""`},
		"testfile":    {"app/greet_test.go", `Start("greeting")`},
		"unrun":       {"app/price.go", "return n / 5 * 5\n"},
		"signature":   {"app/price.go", "type Opt int64\n"},
		"width":       {"limits/limits.go", "const Width = 900\n"},
		"breaks":      {"app/price.go", "type Opt string\n"},
		"newdecl":     {"app/shout.go", "func Shout(s string) string {"},
		"selfdrop":    {"pipelinegen.yaml", "    tests: [\"**/*_test.go\"]\n    exclude: [\"app/greet_test.go\"]\n"},
		"moved":       {"app/greet.go", `return "hi, " + who`},
		"correntry":   {"pipelinegen.yaml", "release: {version: v3-entry, sha256: s3}"},
		"sbomentry":   {"pipelinegen.yaml", "release: {version: v2-entry, sha256: s2}"},
	}

	for name, c := range cases {
		dir := t.TempDir()
		if err := WriteFixture(dir, "/o", "1g", Pins{"v1", "s1", "v2", "s2", "v3", "s3"}); err != nil {
			t.Fatal(err)
		}

		before := ""
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(c.file))); err == nil {
			before = string(data)
		}

		r := &run{pins: map[string]pinned{"go-sbom": {version: "v2"}, "go-correlator": {version: "v3"}}}

		p := &planner{r: r, fixture: dir}
		if err := p.change(t.Context(), name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		after := read(t, filepath.Join(dir, filepath.FromSlash(c.file)))
		if after == before || !strings.Contains(after, c.want) {
			t.Errorf("%s: %s lacks %q:\n%s", name, c.file, c.want, after)
		}
	}
}

const planJSONText = `{"reason": "",
 "suites": [{"suite": "go",
  "selected": [{"name": "report/report_test.go"}, {"name": "app/price_test.go"}],
  "untouched": ["@pipelinegen_deps//slices:app/greet_test.go"],
  "reasons": {"@pipelinegen_deps//slices:app/price_test.go": [{"kind": "bound", "what": "it ran app.Total"}, {"kind": "base-config", "what": "the base's config"}]},
  "narrowed": [{"kind": "sampled", "narrowing": "sample", "off_at": 3, "dropped": ["b", "a"]}, {"kind": "left", "narrowing": "setup-split", "off_at": 5, "dropped": []}]}],
 "plugins": {"warnings": ["go-correlator: app/greet_test.go can't be told"]}}`

func writePlan(t *testing.T, dir, name, text string) string {
	t.Helper()

	p := filepath.Join(dir, "plans", name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestPlanQueries(t *testing.T) {
	p, err := ReadPlan(writePlan(t, t.TempDir(), "x", planJSONText))
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string][2]string{
		"selected":       {p.Selected(), "app/price_test.go,report/report_test.go"},
		"shown":          {p.Shown(), "report/report_test.go, app/price_test.go"},
		"first selected": {p.FirstSelected(), "report/report_test.go"},
		"first left":     {p.FirstUntouched(), "app/greet_test.go"},
		"narrowed":       {p.NarrowedShown(), "sampled sample (left out 2); left setup-split (left out 0)"},
		"sorted lines":   {sortedLines(p), "sample\t3\ta,b\nsetup-split\t5\t"},
		"reasons":        {strings.Join(p.Reasons("app/price_test.go"), "\n"), "it ran app.Total\nthe base's config"},
	}

	for what, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: %q, want %q", what, c[0], c[1])
		}
	}

	if p.Count() != 2 || p.Total() != 3 || p.Sampled("sample") != 1 || p.Sampled("setup-split") != 0 ||
		!p.Warned("app/greet_test.go") || p.Warned("app/price_test.go") || !p.HasReasonKind("app/price_test.go", "base-config") {
		t.Errorf("counts and checks: %+v", p)
	}

	empty, err := ReadPlan(writePlan(t, t.TempDir(), "y", `{"suites": []}`))
	if err != nil || empty.Shown() != "nothing" || empty.NarrowedShown() != "-" || empty.Selected() != "" {
		t.Errorf("an empty plan: %+v %v", empty, err)
	}
}

func testRun(t *testing.T) (*run, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	var out, errs bytes.Buffer

	return &run{o: Options{Stdout: &out, Stderr: &errs}, work: t.TempDir(), walking: true}, &out, &errs
}

func TestVerdicts(t *testing.T) {
	r, _, _ := testRun(t)
	f := writePlan(t, r.work, "both-L2", planJSONText)

	for expectation, want := range map[string]string{
		"exact:app/price_test.go,report/report_test.go": "as expected",
		"exact:app/price_test.go":                       "DIFFERS: expected exact:app/price_test.go",
		"none":                                          "DIFFERS: expected none",
		"all":                                           "DIFFERS: expected all",
		"sample:2:sample":                               "as expected",
		"sample:1:sample":                               "DIFFERS: expected sample:1:sample",
		"own+sample:app/price_test.go:1:sample":         "as expected",
		"own+sample:app/greet_test.go:1:sample":         "DIFFERS: expected own+sample:app/greet_test.go:1:sample",
		"named:2:sample":                                "as expected",
		"named:2:plugin-entry":                          "DIFFERS: expected named:2:plugin-entry",
	} {
		if got := r.verdict(f, expectation, "both-L2"); got != want {
			t.Errorf("%s: %q, want %q", expectation, got, want)
		}
	}

	if n := countLines(filepath.Join(r.work, "failed")); n != 6 {
		t.Errorf("%d differences counted, want 6", n)
	}
}

func TestDropped(t *testing.T) {
	r, _, _ := testRun(t)
	f := writePlan(t, r.work, "start-L2", `{"suites": [{"selected": [{"name": "a"}], "narrowed": [{"kind": "sampled", "narrowing": "sample", "off_at": 3, "dropped": ["c", "b"]}, {"kind": "left", "narrowing": "setup-split", "off_at": 5, "dropped": ["d"]}]}]}`)
	writePlan(t, r.work, "start-L3", `{"suites": [{"selected": [{"name": "a"}, {"name": "b"}, {"name": "c"}]}]}`)
	writePlan(t, r.work, "start-L5", `{"suites": [{"selected": [{"name": "a"}, {"name": "e"}]}]}`)

	if got, want := r.dropped(f), "sample: holds; setup-split: WRONG (says d, level 5 adds e); "; got != want {
		t.Errorf("dropped %q, want %q", got, want)
	}

	g := writePlan(t, r.work, "start-dependents", `{"suites": [{"selected": [{"name": "a"}], "narrowed": [{"kind": "sampled", "narrowing": "sample", "off_at": 3, "dropped": []}]}]}`)
	writePlan(t, r.work, "start-dependents-L3", `{"suites": [{"selected": [{"name": "a"}]}]}`)

	if got := r.dropped(g); got != "sample: holds; " {
		t.Errorf("a dependents plan reads its own level: %q", got)
	}

	h := writePlan(t, r.work, "unused-L2", `{"suites": [{"selected": []}]}`)
	if got := r.dropped(h); got != "-" {
		t.Errorf("no narrowing: %q", got)
	}
}

func TestWalkVerdicts(t *testing.T) {
	r, _, _ := testRun(t)
	plans := filepath.Join(r.work, "plans")

	writePlan(t, r.work, "same", planJSONText)
	writePlan(t, r.work, "same.walk", planJSONText)
	writePlan(t, r.work, "sel", planJSONText)
	writePlan(t, r.work, "sel.walk", `{"suites": [{"selected": []}]}`)
	writePlan(t, r.work, "left", planJSONText)
	writePlan(t, r.work, "left.walk", strings.Replace(planJSONText, `"dropped": ["b", "a"]`, `"dropped": ["a"]`, 1))
	writePlan(t, r.work, "one", planJSONText)

	if err := os.WriteFile(filepath.Join(plans, "one.walk.exit"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(plans, "none.walk.exit"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for label, want := range map[string]string{
		"same": "same",
		"sel":  "DIFFERS: binding forward selects nothing",
		"left": "DIFFERS: binding forward leaves out sample 3 a;setup-split 5 ",
		"one":  "DIFFERS: one tool made no plan (walk exit 1)",
		"none": "same: no plan either way",
	} {
		if got := r.walkverdict(label); got != want {
			t.Errorf("%s: %q, want %q", label, got, want)
		}
	}
}

func TestSummary(t *testing.T) {
	r, stdout, stderr := testRun(t)
	r.pins = map[string]pinned{"pipelinegen": {version: "v1"}, "go-sbom": {version: "v2"}, "go-correlator": {version: "v3"}}
	plans := filepath.Join(r.work, "plans")

	if err := os.MkdirAll(plans, 0o750); err != nil {
		t.Fatal(err)
	}

	for name, text := range map[string]string{"a-L2.walk": "same\n", "b-L2.walk": "same\n", "table.md": "| a | x | as expected |\n"} {
		dir := plans
		if name == "table.md" {
			dir = r.work
		}

		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.summary(); err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "pinned releases: pipelinegen v1, go-sbom v2, go-correlator v3: 0 rows differ from the table; binding forward, built from this checkout, differs in 0 of 2 plans\n"; got != want {
		t.Errorf("the last line %q, want %q", got, want)
	}

	if got := stderr.String(); got != "0 differences\n" {
		t.Errorf("stderr %q", got)
	}

	r, stdout, _ = testRun(t)
	r.o.Source, r.built = true, "abc with local edits"
	r.pins = map[string]pinned{"go-sbom": {version: "v2"}, "go-correlator": {version: "v3"}}

	if err := os.MkdirAll(filepath.Join(r.work, "plans"), 0o750); err != nil {
		t.Fatal(err)
	}

	for name, text := range map[string]string{"plans/a-L2.walk": "DIFFERS: binding forward selects nothing\n", "table.md": "| a-L2 | x | DIFFERS: expected none |\n", "failed": "x\n"} {
		if err := os.WriteFile(filepath.Join(r.work, filepath.FromSlash(name)), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.summary(); err == nil {
		t.Error("a proof that differs passed")
	}

	want := "plans the forward binding (built from this checkout) plans differently:\na-L2 DIFFERS: binding forward selects nothing\n" +
		"the tool built from this commit differs from the table in 1 rows; the change, not a release, breaks them:\n| a-L2 | x | DIFFERS: expected none |\n" +
		"the tool built from this commit (abc with local edits), with the pinned go-sbom v2, go-correlator v3: 1 rows differ from the table; binding forward, built from this checkout, differs in 1 of 1 plans\n"
	if got := stdout.String(); got != want {
		t.Errorf("source mode:\n%s\nwant\n%s", got, want)
	}
}

func TestHelpers(t *testing.T) {
	if got := minus("a,b,c", "b"); got != "a,c" {
		t.Errorf("minus %q", got)
	}

	if got := minus("", "b"); got != "" {
		t.Errorf("minus of nothing %q", got)
	}

	if n := countUnits("# a list\nx.F\ny.G\n"); n != 2 {
		t.Errorf("units %d", n)
	}

	if n := countUnits(""); n != 0 {
		t.Errorf("no units %d", n)
	}

	if !has("a,b", "b") || has("a,bc", "b") {
		t.Error("has")
	}

	env := setEnv([]string{"A=1", "B=2"}, "A=3", "C=4")
	if strings.Join(env, " ") != "B=2 A=3 C=4" || envOf(env, "A") != "3" || strings.Join(unsetEnv(env, "B"), " ") != "A=3 C=4" {
		t.Errorf("env %v", env)
	}
}

func TestPins(t *testing.T) {
	pins, err := readPins(filepath.Join("..", "..", "ci", "proof", "pins"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range pieces {
		if p := pins[name]; p.repo == "" || !strings.HasPrefix(p.version, "v") || strings.Contains(p.version, "/") || len(p.sha) != 64 {
			t.Errorf("%s: %+v", name, p)
		}
	}

	if p := pins[pieceSBOM]; p.path != "sbom/go" || p.tag() != "sbom/go/"+p.version {
		t.Errorf("go-sbom's release in the plugins repository: %+v, tag %s", p, p.tag())
	}

	if p := pins["pipelinegen"]; p.path != "" || p.tag() != p.version {
		t.Errorf("pipelinegen's release: %+v, tag %s", p, p.tag())
	}
}

// fixtureSums are the fixture's files by SHA-256 as the proof has always written them (pins tv, ts, sv,
// ss, cv, cs, output root /out/root, heap 2g): the tables are about this fixture.
var fixtureSums = map[string]string{
	"app/config.go":         "32d81a9feef7d5c16c9924928607fc64e46be2af7e8c58bd2497715a98270b1a",
	"app/greet.go":          "0e178790d9a18c9f4d516ac8bd2139a529c6d502ae7bf6c95aa2319b8c4c6309",
	"app/greet_test.go":     "55a4547cfc0f502c49292619525bb1f7e35eacd9a863f5e34e713fe01c9eb7d8",
	"app/price.go":          "52d4351314cc1a46b82e1db1e9cd236f35b630fb657b5d3e527f107e7e11165e",
	"app/price_test.go":     "5995c265493517720b2c2fcae7d9f79735518bc6c4f359573cabcc0d26c1baa3",
	"app/registry.go":       "a5127855e2068c8649f195b63f0cd3494448a74615967b3ae4df0940a56a6ff0",
	"app/start.go":          "193c6cfaccce85662387d3a1f3180eb41b8b2f96f4c25c308b11a501acf4d1da",
	".bazelignore":          "a2d46f93c6735f99a06593688e323720474286ee331f92eae9a886a14457e17a",
	".bazelrc":              "0efb1eb7e64263bc30fbfbcae6af616689b67f2d5c0103736b725451a2402ead",
	".bazelversion":         "1b9487d55bea47fea50d226cc9c53bc548877ad2a318bac8fbc8b320f429e5c5",
	"BUILD.bazel":           "94f6e2d42c1ac97a58a03ec0c19ee001b7610e0a1e5cd128d3e9d0a1b1af8276",
	"ci/go-batch.sh":        "ad1b422dad0683b2d77da7b8b34572d167a87d531305e4261676202de67dd178",
	"ci/go-batch.yml":       "415985367ca2e73522a9648499167b45159efade0a7cefa37f49d929435e11ee",
	".gitignore":            "c477afda3628fcd14135c9630291e7a1f6b335ef71eec1745a7a3b6c46e037f0",
	"go.mod":                "8d0b1f175df3208d706be7fac8f1198411f20a64c5caab38abf2d6dd422a715e",
	"limits/limits.go":      "8ec19d1b11484b98aeec75942f6f34cc2a81d65323ebf6a0b90f905d87a0ab60",
	"MODULE.bazel":          "3049d10ab95b836c248bf62dbd574b85db54d809e2f6f70d307db3990bdb8177",
	"pad/pad.go":            "3ef9469fc762e2c80233e4e84874363772be33c8834f912becd6a6cd9e61c2f7",
	"pad/pad.s":             "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"pipelinegen.yaml":      "765af2f62e66b8f3db7b4e30651e8b01ee1801650a127d9335a4068d8ab4067c",
	"report/report.go":      "a34ca59d5488f7a83928077a962eca30df6bbe4c14814b1d581aa967335e8877",
	"report/report_test.go": "a1989931bfa62de210a5e2bb4bb6fce809f2e82a0d28e33bd87945112dbe6411",
	"unused/unused.go":      "0524e0f696eb459c476a663900412b430e76a6fbfca2a11a0aa16410622fe9df",
}

func TestTheFixtureIsTheProofsOwn(t *testing.T) {
	dir := t.TempDir()

	if err := WriteFixture(dir, "/out/root", "2g", Pins{"tv", "ts", "sv", "ss", "cv", "cs"}); err != nil {
		t.Fatal(err)
	}

	seen := 0

	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.Name() == ".git" {
				return filepath.SkipDir
			}

			return err
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(p) //nolint:gosec // the test's own fixture
		if err != nil {
			t.Fatal(err)
		}

		seen++

		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != fixtureSums[filepath.ToSlash(rel)] {
			t.Errorf("%s differs from the proof's fixture:\n%s", rel, data)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if seen != len(fixtureSums) {
		t.Errorf("%d files, the fixture has %d", seen, len(fixtureSums))
	}
}
