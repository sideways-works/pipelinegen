package bazel_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestOpenGroups(t *testing.T) {
	testrepo.NeedBazel(t)

	r := testrepo.New(t)
	r.Write("g/BUILD.bazel", `filegroup(name = "top", srcs = [":inner", "x.txt", "BUILD.bazel", ":gen", ":al", "//h:sel", ":og", "@bazel_tools//tools/build_defs/repo:local.bzl"], data = ["y.txt"])
filegroup(name = "inner", srcs = glob(["m/*.sql"]))
genrule(name = "gen", outs = ["g.txt"], cmd = "echo > $@")
alias(name = "al", actual = "//h:plain")
filegroup(name = "og", srcs = [":gen"], output_group = "foo")
`)
	r.Write("h/BUILD.bazel", `config_setting(name = "c", values = {"compilation_mode": "dbg"})
filegroup(name = "sel", srcs = select({":c": ["s1.txt"], "//conditions:default": ["s2.txt"]}), visibility = ["//visibility:public"])
filegroup(name = "plain", srcs = ["p.txt", "//g:g.txt"], visibility = ["//visibility:public"])
`)

	for _, f := range []string{"g/x.txt", "g/y.txt", "g/m/1.sql", "g/m/2.sql", "h/s1.txt", "h/s2.txt", "h/p.txt"} {
		r.Write(f, "x\n")
	}

	r.Commit("groups")

	c := testrepo.Bazel(t, r.Dir)

	g, err := c.OpenGroups(t.Context(), []string{"//g:top", "//g:missing", "//g:x.txt"})
	if err != nil {
		t.Fatal(err)
	}

	got := g.Leaves("//g:top")
	want := map[string][]string{
		"//g:m/1.sql":     {"//g:inner", "//g:top"},
		"//g:m/2.sql":     {"//g:inner", "//g:top"},
		"//g:x.txt":       {"//g:top"},
		"//g:y.txt":       {"//g:top"},
		"//g:BUILD.bazel": {"//g:top"},
		"//g:gen":         {"//g:top"},
		"//h:p.txt":       {"//h:plain", "//g:al", "//g:top"},
		"//h:sel":         {"//g:top"},
		"//g:og":          {"//g:top"},
		"@bazel_tools//tools/build_defs/repo:local.bzl": {"//g:top"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("leaves:\n got %v\nwant %v", got, want)
	}

	if !slices.Equal(slices.Sorted(maps.Keys(g.Leaves("//g:missing"))), []string{"//g:missing"}) {
		t.Errorf("a label the tree lacks stays whole: %v", g.Leaves("//g:missing"))
	}

	if chain, ok := g.Through("//g:top", "//g:m/2.sql"); !ok || !slices.Equal(chain, []string{"//g:inner", "//g:top"}) {
		t.Errorf("through %v %v", chain, ok)
	}

	if g.IsGroup("//h:sel") || g.IsGroup("//g:og") || g.IsGroup("//g:gen") {
		t.Errorf("a select(), an output_group and another rule stay whole: %v", g.Members)
	}
}

func TestMainCanonical(t *testing.T) {
	for in, want := range map[string]string{"//a/b": "//a/b:b", "@@//a:c": "//a:c", "@//:x": "//:x"} {
		if got, ok := bazel.MainCanonical(in); !ok || got != want {
			t.Errorf("%s: %s %v", in, got, ok)
		}
	}

	for _, in := range []string{"@r//a:b", "a:b", "//"} {
		if _, ok := bazel.MainCanonical(in); ok {
			t.Errorf("%s has a main label", in)
		}
	}
}
