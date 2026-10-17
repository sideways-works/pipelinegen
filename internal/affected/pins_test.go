package affected

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const pinModule = `module(name = "fixture")

bazel_dep(name = "tools", version = "%s")
bazel_dep(name = "rules_x", version = "1.0", repo_name = "x")
`

const pinLock = `{
  "lockFileVersion": 26,
  "registryFileHashes": {
    "https://registry.example/modules/tools/%[1]s/MODULE.bazel": "aa%[1]s",
    "https://registry.example/modules/tools/%[1]s/source.json": "bb%[1]s",
    "https://registry.example/modules/rules_x/1.0/MODULE.bazel": "cc"
  }
}
`

func TestAnotherRepositorysInputIsJudgedByItsPin(t *testing.T) {
	r := testrepo.New(t)
	write := func(version, extra string) string {
		t.Helper()
		r.Write("MODULE.bazel", strings.ReplaceAll(pinModule, "%s", version)+extra)
		r.Write("MODULE.bazel.lock", strings.ReplaceAll(pinLock, "%[1]s", version))
		// the fixture ignores its lock, which a repository commits
		r.Git("add", "-f", "MODULE.bazel.lock")

		return r.Commit("tools " + version)
	}

	base := write("1.0", "")

	r.Write("web/app.ts", "changed\n")
	same := r.Commit("unrelated")

	bumped := write("1.1", "")
	local := write("1.1", "local_path_override(module_name = \"tools\", path = \"../tools\")\n")

	reasons := func(b, h string, labels ...string) []Reason {
		t.Helper()

		o := Options{
			Repo:   gitx.Repo{Dir: r.Dir},
			Inputs: func(string, string, string) ([]string, error) { return labels, nil },
		}
		o.pinned = o.pinJudge(t.Context(), b, h)

		rs, err := o.inputReasons("e2e", "a.spec.ts", b, h, nil, func() (map[string]bool, error) { return map[string]bool{}, nil }, false)
		if err != nil {
			t.Fatal(err)
		}

		return rs
	}

	for _, l := range []string{"@tools//lint:catalog.yaml", "@@tools+//lint:catalog.yaml", "@@tools~//lint:catalog.yaml"} {
		if rs := reasons(base, same, l); len(rs) != 0 {
			t.Errorf("%s under the same pin: %+v", l, rs)
		}

		if rs := reasons(base, bumped, l); len(rs) != 1 || rs[0].Kind != KindTarget || rs[0].Through[0] != "the pin of its repository changed" {
			t.Errorf("%s bumped: %+v", l, rs)
		}
	}

	for name, c := range map[string]struct{ b, h, label, why string }{
		"a local override":           {bumped, local, "@tools//lint:catalog.yaml", "a local_path_override of tools"},
		"an extension's repository":  {base, same, "@@tools++ext+npm//:a.json", "nothing the plan reads pins it"},
		"a repository no dep stands": {base, same, "@npm//:a.json", "nothing the plan reads pins it"},
	} {
		rs := reasons(c.b, c.h, c.label)
		if len(rs) != 1 || rs[0].Kind != KindInputUnknown || len(rs[0].Through) != 1 || !strings.Contains(rs[0].Through[0], c.why) {
			t.Errorf("%s: %+v", name, rs)
		}
	}
}

func TestAModuleWithoutALockfileIsPinnedByItsVersionAndRegistry(t *testing.T) {
	r := testrepo.New(t)
	commit := func(version, registry, extra string) string {
		t.Helper()
		r.Write("MODULE.bazel", strings.ReplaceAll(pinModule, "%s", version)+extra)
		r.Write(".bazelrc", "common --registry="+registry+"\ncommon --registry=https://bcr.bazel.build\n")

		return r.Commit("tools " + version + " from " + registry)
	}

	const archive = `http_archive = use_repo_rule("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(name = "assets", urls = ["https://example.com/a.tgz"], sha256 = "%s")
http_archive(name = "loose", urls = ["https://example.com/b.tgz"])
`

	base := commit("1.0", "https://registry.example", strings.ReplaceAll(archive, "%s", "aa"))

	r.Write("web/app.ts", "changed\n")
	same := r.Commit("unrelated")
	bumped := commit("1.1", "https://registry.example", strings.ReplaceAll(archive, "%s", "aa"))
	moved := commit("1.1", "https://other.example", strings.ReplaceAll(archive, "%s", "bb"))

	var warned []string

	reasons := func(b, h, label string) []Reason {
		t.Helper()

		o := Options{
			Repo:   gitx.Repo{Dir: r.Dir},
			Inputs: func(string, string, string) ([]string, error) { return []string{label}, nil },
			Warn:   func(f string, a ...any) { warned = append(warned, fmt.Sprintf(f, a...)) },
		}
		o.pinned = o.pinJudge(t.Context(), b, h)

		rs, err := o.inputReasons("go", "a_test.go", b, h, nil, func() (map[string]bool, error) { return map[string]bool{}, nil }, false)
		if err != nil {
			t.Fatal(err)
		}

		return rs
	}

	if rs := reasons(base, same, "@tools//lint:catalog.yaml"); len(rs) != 0 {
		t.Errorf("same version and registry: %+v", rs)
	}

	if !slices.ContainsFunc(warned, func(w string) bool { return strings.Contains(w, "@tools is pinned by version (no lockfile hash)") }) {
		t.Errorf("no word of the version-only pin: %q", warned)
	}

	for name, h := range map[string]string{"a bump": bumped, "another registry": moved} {
		if rs := reasons(base, h, "@tools//lint:catalog.yaml"); len(rs) != 1 || rs[0].Kind != KindTarget {
			t.Errorf("%s: %+v", name, rs)
		}
	}

	if rs := reasons(base, same, "@assets//:logo.svg"); len(rs) != 0 {
		t.Errorf("an archive under the same sha256: %+v", rs)
	}

	if rs := reasons(bumped, moved, "@assets//:logo.svg"); len(rs) != 1 || rs[0].Kind != KindTarget {
		t.Errorf("an archive with another sha256: %+v", rs)
	}

	if rs := reasons(base, same, "@loose//:b.json"); len(rs) != 1 || rs[0].Kind != KindInputUnknown || !strings.Contains(rs[0].Through[0], "the repository loose (http_archive) has no sha256, integrity or commit") {
		t.Errorf("an archive without a hash: %+v", rs)
	}
}
