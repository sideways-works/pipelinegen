package buildfile

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type memTree map[string]string

func (m memTree) files() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}

	slices.Sort(out)

	return out
}

func (m memTree) read(p string) ([]byte, error) {
	s, ok := m[p]
	if !ok {
		return nil, os.ErrNotExist
	}

	return []byte(s), nil
}

func load(t *testing.T, m memTree) *Graph {
	t.Helper()

	g := Load(m.files(), m.read)
	for _, err := range g.Errors {
		t.Errorf("load: %v", err)
	}

	return g
}

func target(t *testing.T, g *Graph, label string) *Target {
	t.Helper()

	tg := g.Targets[label]
	if tg == nil {
		t.Fatalf("no target %s among %v", label, slices.Sorted(mapKeys(g.Targets)))
	}

	return tg
}

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		":x":                    "//p/q:x",
		"//a/b":                 "//a/b:b",
		"//a/b:c":               "//a/b:c",
		"//:root":               "//:root",
		"@//a:b":                "//a:b",
		"@@//a":                 "//a:a",
		"@rules_go//go:def.bzl": "@rules_go//go:def.bzl",
		"@com_github_x_y//sub":  "@com_github_x_y//sub:sub",
		"@repo":                 "@repo//:repo",
	}
	for in, want := range cases {
		if got, ok := Canonical("p/q", in); !ok || got != want {
			t.Errorf("%s: got %q %v, want %q", in, got, ok, want)
		}
	}

	if _, ok := Canonical("p", "file.go"); ok {
		t.Error("a bare name is no label")
	}
}

func TestTargetsGlobsAndInputs(t *testing.T) {
	g := load(t, memTree{
		"BUILD.bazel":         `exports_files(["root.txt"])`,
		"root.txt":            "",
		"web/BUILD.bazel":     `filegroup(name = "web", srcs = glob(["**/*.ts"], exclude = ["**/*.spec.ts"]), visibility = ["//visibility:public"])`,
		"web/app.ts":          "",
		"web/app.spec.ts":     "",
		"web/lib/util.ts":     "",
		"web/sub/BUILD.bazel": `filegroup(name = "sub", srcs = ["x.ts"])`,
		"web/sub/x.ts":        "",
		"e2e/BUILD.bazel": `
SPECS = ["checkout.spec.ts"]

filegroup(
    name = "checkout",
    srcs = SPECS + ["helpers"],
    data = ["//web", "@npm//:playwright", "//:root.txt", ":nothing-here"] + select({"//conditions:default": [":fixtures"]}),
    tags = ["slice-e2e"],
)

filegroup(name = "helpers", srcs = ["helpers.ts"])
filegroup(name = "fixtures", srcs = glob(["fixtures/*"]))
`,
		"e2e/checkout.spec.ts":   "",
		"e2e/helpers.ts":         "",
		"e2e/fixtures/a.json":    "",
		"e2e/fixtures/deep/b.js": "",
	})

	if got := target(t, g, "//web:web").Attrs["srcs"]; !reflect.DeepEqual(got, []string{"//web:app.ts", "//web:lib/util.ts"}) {
		t.Errorf("web srcs: %v (a subpackage's files and excluded ones stay out)", got)
	}

	co := target(t, g, "//e2e:checkout")
	if !reflect.DeepEqual(co.Tags(), []string{"slice-e2e"}) || co.Kind != "filegroup" {
		t.Errorf("checkout: %v %s", co.Tags(), co.Kind)
	}

	want := []string{"//:root.txt", "//e2e:checkout.spec.ts", "//e2e:fixtures", "//e2e:helpers", "//web:web", "@npm//:playwright"}
	if got := co.Inputs(); !reflect.DeepEqual(got, want) {
		t.Errorf("inputs\n got %v\nwant %v", got, want)
	}

	if got := target(t, g, "//e2e:fixtures").Attrs["srcs"]; !reflect.DeepEqual(got, []string{"//e2e:fixtures/a.json"}) {
		t.Errorf("fixtures: %v", got)
	}
}

func TestFileLabelsAndPackages(t *testing.T) {
	g := load(t, memTree{
		"BUILD":         "",
		"a/BUILD.bazel": "",
		"a/BUILD":       "this is not starlark (",
		"a/b/c.go":      "",
		"a/b/d/BUILD":   "",
		"top.md":        "",
	})

	if g.Packages["a"] != "a/BUILD.bazel" {
		t.Errorf("BUILD.bazel wins over BUILD: %v", g.Packages)
	}

	for p, want := range map[string]string{"a/b/c.go": "//a:b/c.go", "top.md": "//:top.md"} {
		if got, ok := g.FileLabel(p); !ok || got != want {
			t.Errorf("%s: %s", p, got)
		}
	}

	if p, ok := g.LabelFile("//a:b/c.go"); !ok || p != "a/b/c.go" {
		t.Errorf("label file: %s %v", p, ok)
	}
}

func TestMacrosOfTheRepositoryAreExpanded(t *testing.T) {
	g := load(t, memTree{
		"api/BUILD.bazel": `
load(":pipelinegen_slices.bzl", "slices")
load("//tools:defs.bzl", "spec", tagged = "with_tag")
load("@rules_go//go:def.bzl", "go_test")

slices()

spec(name = "smoke", file = "smoke_test.go")

tagged(name = "opaque", srcs = ["a_test.go"])

go_test(name = "api_test", srcs = glob(["*_test.go"]), tags = ["manual"])
`,
		"api/pipelinegen_slices.bzl": `
load("@rules_go//go:def.bzl", "go_test")

def slices():
    native.filegroup(
        name = "slice-orders",
        srcs = ["a_test.go"],
        data = ["@pipelinegen_deps//slices:api.slice-orders"],
        tags = ["slice-backend", "pipelinegen-run://api:slice-orders_test"],
    )
    go_test(
        name = "slice-orders_test",
        srcs = ["a_test.go"],
        tags = ["manual"],
    )
`,
		"tools/BUILD.bazel": "",
		"tools/defs.bzl": `
DEFAULT_TAGS = ["slice-backend"]

def spec(name, file, tags = DEFAULT_TAGS):
    native.filegroup(name = name, srcs = [file], tags = tags)

def with_tag(**kwargs):
    pass
`,
		"api/a_test.go":     "",
		"api/smoke_test.go": "",
	})

	sel := target(t, g, "//api:slice-orders")
	if sel.File != "api/pipelinegen_slices.bzl" || !slices.Contains(sel.Tags(), "pipelinegen-run://api:slice-orders_test") {
		t.Errorf("selector: %+v", sel)
	}

	if got := target(t, g, "//api:slice-orders_test").Kind; got != "go_test" {
		t.Errorf("twin kind %s", got)
	}

	smoke := target(t, g, "//api:smoke")
	if !reflect.DeepEqual(smoke.Tags(), []string{"slice-backend"}) || !reflect.DeepEqual(smoke.Attrs["srcs"], []string{"//api:smoke_test.go"}) {
		t.Errorf("smoke: %+v", smoke)
	}

	if got := target(t, g, "//api:opaque").Inputs(); !reflect.DeepEqual(got, []string{"//api:a_test.go"}) {
		t.Errorf("opaque macro inputs: %v", got)
	}

	if !reflect.DeepEqual(g.Loads["api"], []string{"api/pipelinegen_slices.bzl", "tools/defs.bzl"}) {
		t.Errorf("loads: %v", g.Loads["api"])
	}
}

func TestUnreadableFilesAreErrors(t *testing.T) {
	m := memTree{"a/BUILD": "filegroup(name = ", "b/BUILD": `load(":x.bzl", "m")` + "\nm()", "b/x.bzl": "def m(:"}
	g := Load(m.files(), m.read)

	if len(g.Errors) != 2 {
		t.Fatalf("errors: %v", g.Errors)
	}

	for _, err := range g.Errors {
		if !errors.Is(err, ErrParse) {
			t.Errorf("not ErrParse: %v", err)
		}
	}
}

func TestTheStaticSubsetIsRead(t *testing.T) {
	g := load(t, memTree{
		"lists/BUILD.bazel": "",
		"lists/inputs.bzl": `load(":more.bzl", "MORE")

BASE = ["a.txt", "b.txt"]
ALL = BASE + MORE
SUFFIX = "_test"
`,
		"lists/more.bzl": `MORE = ["c.txt"]`,
		"p/BUILD.bazel": `load("//lists:inputs.bzl", "ALL", "BASE", "SUFFIX")
load(":defs.bzl", "slice")

filegroup(name = "dedup", srcs = list({f: None for f in BASE + ALL}))
filegroup(name = "plain", srcs = [f for f in ALL])
filegroup(name = "nested", srcs = [d + "/" + f for d in ["x", "y"] for f in ["1.txt"]])
filegroup(name = "keys", srcs = {"a.txt": "x", "b.txt": "y"}.keys())
filegroup(name = "cond", srcs = ["a.txt"] if True else ["b.txt"])
filegroup(name = "sel", srcs = ["a.txt"] + select({"//conditions:default": ["c.txt"]}))
filegroup(name = "spaced", srcs = ["Noto Sans/0-255.pbf", "f (1).txt"])
filegroup(name = "named" + SUFFIX, srcs = ["a.txt"])

slice(name = "wrapped", data = ["b.txt"])
`,
		"p/defs.bzl": `def slice(name, tags = [], **kwargs):
    native.filegroup(name = name, tags = tags + ["slice"], **kwargs)
`,
		"p/a.txt": "", "p/b.txt": "", "p/c.txt": "",
		"p/x/1.txt": "", "p/y/1.txt": "",
		"p/Noto Sans/0-255.pbf": "", "p/f (1).txt": "",
	})

	cases := map[string][]string{
		"//p:dedup":      {"//p:a.txt", "//p:b.txt", "//p:c.txt"},
		"//p:plain":      {"//p:a.txt", "//p:b.txt", "//p:c.txt"},
		"//p:nested":     {"//p:x/1.txt", "//p:y/1.txt"},
		"//p:keys":       {"//p:a.txt", "//p:b.txt"},
		"//p:cond":       {"//p:a.txt", "//p:b.txt"},
		"//p:sel":        {"//p:a.txt", "//p:c.txt"},
		"//p:spaced":     {"//p:Noto Sans/0-255.pbf", "//p:f (1).txt"},
		"//p:named_test": {"//p:a.txt"},
	}
	for label, want := range cases {
		tg := target(t, g, label)
		if got := tg.Inputs(); !reflect.DeepEqual(got, want) || len(tg.Unreadable) != 0 {
			t.Errorf("%s: got %v (unreadable %v), want %v", label, got, tg.Unreadable, want)
		}
	}

	if got := target(t, g, "//p:dedup").Strings["srcs"]; len(got) != 3 {
		t.Errorf("a deduplicating comprehension keeps each label once: %v", got)
	}

	w := target(t, g, "//p:wrapped")
	if !reflect.DeepEqual(w.Inputs(), []string{"//p:b.txt"}) || !reflect.DeepEqual(w.Tags(), []string{"slice"}) {
		t.Errorf("**kwargs passed on: %v %v", w.Inputs(), w.Tags())
	}
}

func TestWhatCantBeReadSaysWhy(t *testing.T) {
	m := memTree{
		"p/BUILD.bazel": `load("//p:gen.bzl", "GENERATED")
load(":defs.bzl", "helper")

filegroup(name = "call", srcs = helper())
filegroup(name = "undefined", srcs = NOWHERE)
filegroup(name = "untracked", srcs = GENERATED)
filegroup(name = "filtered", srcs = [f for f in ["a"] if f])
filegroup(name = "format", srcs = ["%s.txt" % "a"])
filegroup(name = "index", srcs = ["a.txt"][0:1])
filegroup(name = "fine", srcs = ["a.txt"], shard_count = 4, testonly = True)
filegroup(name = NAME_FROM_NOWHERE, srcs = ["a.txt"])
`,
		"p/defs.bzl": "def helper():\n    return []\n",
		"p/a.txt":    "",
	}
	g := Load(m.files(), m.read)

	for label, why := range map[string]string{
		"//p:call":      "helper(...) cannot be read",
		"//p:undefined": "NOWHERE is not defined",
		"//p:untracked": "GENERATED comes from //p:gen.bzl, which is not a file of the repository",
		"//p:filtered":  "a comprehension's if other than string tests",
		"//p:format":    "the operator % cannot be read",
		"//p:index":     "cannot be read without running Starlark",
	} {
		if got := target(t, g, label).Unreadable["srcs"]; !strings.Contains(got, why) {
			t.Errorf("%s: %q, want it to say %q", label, got, why)
		}
	}

	if u := target(t, g, "//p:fine").Unreadable; len(u) != 0 {
		t.Errorf("numbers and booleans are readable: %v", u)
	}

	if len(g.Errors) != 1 || !strings.Contains(g.Errors[0].Error(), "the name can't be read: NAME_FROM_NOWHERE is not defined") {
		t.Errorf("errors: %v", g.Errors)
	}
}

func TestAComprehensionFilteredByStringTestsIsRead(t *testing.T) {
	g := load(t, memTree{
		"lists/BUILD.bazel": "",
		"lists/all.bzl":     `EVERY = ["//docs:guide", "//web:app", "//web:app_test", "//api:server", "//api:server.go"]` + "\n",
		"p/BUILD.bazel": `load("//lists:all.bzl", "EVERY")

filegroup(name = "not_docs", data = [t for t in EVERY if not t.startswith("//docs:")])
filegroup(name = "web", data = [t for t in EVERY if t.startswith(("//web:",)) and not t.endswith("_test")])
filegroup(name = "picked", data = [t for t in EVERY if t in ["//api:server", "//docs:guide"] or t == "//web:app"])
filegroup(name = "substring", data = [t for t in EVERY if ".go" not in t and "api" in t])
`,
		"docs/BUILD.bazel": `filegroup(name = "guide")`,
		"web/BUILD.bazel":  `filegroup(name = "app")` + "\n" + `filegroup(name = "app_test")`,
		"api/BUILD.bazel":  `filegroup(name = "server")` + "\n" + `exports_files(["server.go"])`,
		"api/server.go":    "",
	})

	for label, want := range map[string][]string{
		"//p:not_docs":  {"//api:server", "//api:server.go", "//web:app", "//web:app_test"},
		"//p:web":       {"//web:app"},
		"//p:picked":    {"//api:server", "//docs:guide", "//web:app"},
		"//p:substring": {"//api:server"},
	} {
		tg := target(t, g, label)
		if got := tg.Inputs(); !reflect.DeepEqual(got, want) || len(tg.Unreadable) != 0 {
			t.Errorf("%s: got %v (unreadable %v), want %v", label, got, tg.Unreadable, want)
		}
	}
}

func TestNamesFromOtherRepositoriesNameTools(t *testing.T) {
	m := memTree{
		"MODULE.bazel": "module(name = \"sample\", repo_name = \"alias\")\n",
		"p/BUILD.bazel": `load("@npm//p:tool/package_json.bzl", tool_bin = "bin")
load("@sample//p:defs.bzl", "OWN")
load("@alias//p:defs.bzl", OTHER = "OWN")

filegroup(name = "plain", srcs = ["a.txt"], tool = tool_bin)
filegroup(name = "dotted", srcs = OWN, tool = tool_bin.tool_binary)
filegroup(name = "mixed", srcs = OTHER + [tool_bin], data = select({"//conditions:default": [tool_bin]}))
filegroup(name = "own", srcs = ["@sample//p:a.txt"])
`,
		"p/defs.bzl": "OWN = [\"a.txt\"]\n",
		"p/a.txt":    "",
	}
	g := load(t, m)

	const tool = "@npm//p:tool/package_json.bzl"

	for label, want := range map[string]struct {
		inputs  []string
		outside []string
	}{
		"//p:plain":  {[]string{"//p:a.txt", tool}, []string{"tool"}},
		"//p:dotted": {[]string{"//p:a.txt", tool}, []string{"tool"}},
		"//p:mixed":  {[]string{"//p:a.txt", tool}, []string{"data", "srcs"}},
		"//p:own":    {[]string{"//p:a.txt"}, nil},
	} {
		tg := target(t, g, label)
		if got := tg.Inputs(); !reflect.DeepEqual(got, want.inputs) || len(tg.Unreadable) != 0 {
			t.Errorf("%s: inputs %v (unreadable %v), want %v", label, got, tg.Unreadable, want.inputs)
		}

		if got := slices.Sorted(mapKeys(tg.Outside)); !slices.Equal(got, want.outside) {
			t.Errorf("%s: outside %v, want %v", label, got, want.outside)
		}
	}
}
