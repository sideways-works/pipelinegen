package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const aloneConfig = `pipeline: alone
github: {repository: owner/alone, token: github_token}
concourse:
  url: https://ci.example
  team: alone
  repository: {uri: "https://github.com/owner/alone.git", password: ((github_token))}
  planner:
    image: {repository: golang, tag: "1.27.1"}
    digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    bootstrap: {commit: 0123456789abcdef0123456789abcdef01234567, bazelisk: {version: v1.27.0, sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}}
suites:
  unit:
    tests: ["web/**/*.test.ts"]
    run_slice: ci/slice.yml
  missing:
    tests: ["miss/*.test.ts"]
    owner: "//{dir}:{stem}"
    run_slice: ci/slice.yml
  extra:
    tests: ["extra/*.test.ts"]
    extra_deps: [{tests: ["extra/*.test.ts"], deps: ["//other"]}]
    run_slice: ci/slice.yml
runs:
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {unit: {}, missing: {}, extra: {}}
everything_on: [pipelinegen.yaml, MODULE.bazel, .bazelversion, "ci/**"]
`

func aloneRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	dir := t.TempDir()

	for name, text := range map[string]string{
		"MODULE.bazel": "module(name = \"alone\")\n\n" +
			"local_repository = use_repo_rule(\"@bazel_tools//tools/build_defs/repo:local.bzl\", \"local_repository\")\n\n" +
			"local_repository(name = \"pipelinegen_deps\", path = \".pipelinegen/deps\")\n",
		".bazelversion":        "9.2.0\n",
		".bazelignore":         ".pipelinegen\n",
		".gitignore":           ".pipelinegen/\nbazel-*\nMODULE.bazel.lock\n",
		"pipelinegen.yaml":     aloneConfig,
		"ci/slice.yml":         "platform: linux\n",
		"lib/BUILD.bazel":      "filegroup(name = \"lib\", srcs = [\"api.ts\"], visibility = [\"//visibility:public\"])\n",
		"lib/api.ts":           "export const api = 1;\n",
		"other/BUILD.bazel":    "filegroup(name = \"other\", srcs = [\"other.ts\"], visibility = [\"//visibility:public\"])\n",
		"other/other.ts":       "export const other = 1;\n",
		"web/BUILD.bazel":      "filegroup(name = \"web\", srcs = [\"index.ts\"])\n",
		"web/index.ts":         "export const web = 1;\n",
		"web/src/page.test.ts": "import { api } from '../../lib/api';\n",
		"web/src/form.test.ts": "import { api } from '../../lib/api';\n",
		"miss/x.test.ts":       "import { api } from '../lib/api';\n",
		"extra/e.test.ts":      "import { other } from '../other/other';\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return testrepo.NewFrom(t, dir)
}

func TestTestFilesNothingHoldsRunOnEveryChange(t *testing.T) {
	// The switch is package-wide: no other test may run while it is on.
	before := forward.Load()

	t.Cleanup(func() { forward.Store(before) })

	r := aloneRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Write("lib/api.ts", "export const api = 2;\n")
	head := r.Commit("change the module the tests import")

	differ := bazeldifftest.Tool(t).Path
	pdir := pluginsDirFor(t)

	for _, walking := range []bool{true, false} {
		cache := t.TempDir()
		bz := testrepo.Bazel(t, filepath.Join(cache, "tree"))

		cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
		if err != nil {
			t.Fatal(err)
		}

		cfg.Bazel.Bazelisk, cfg.Bazel.StartupFlags = "bazelisk", bz.StartupFlags

		forward.Store(!walking)

		p, err := affectedPlan(t.Context(), cfg, r.Dir, base, head, false, cache, differ, listsNone, pdir)
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("walking %v: everything: %s", walking, p.Reason)
		}

		got := map[string]string{}
		for _, sp := range p.Suites {
			got[sp.Suite] = selectedNames(sp)

			for _, sl := range sp.Selected {
				if rs := sp.Reasons[sl.Label]; !slices.ContainsFunc(rs, func(r affected.Reason) bool {
					return r.Kind == affected.KindEverything && r.What == affected.AloneReason
				}) {
					t.Errorf("walking %v: %s/%s: reasons %+v", walking, sp.Suite, sl.Name, rs)
				}
			}
		}

		want := map[string]string{"unit": "web/src/form.test.ts,web/src/page.test.ts", "missing": "miss/x.test.ts", "extra": ""}
		for suite, w := range want {
			if got[suite] != w {
				t.Errorf("walking %v: %s selects %q, want %q", walking, suite, got[suite], w)
			}
		}

		warning := "suite unit: nothing binds 2 of its test files but the files themselves (web/src/form.test.ts, web/src/page.test.ts): " +
			"no owner target, Bazel target, extra_deps entry or pipelinegen:data line holds them, so they run on every change; " +
			"give the suite an owner, a target that holds them, or bind: recorded with a correlator that binds them"
		if !slices.Contains(p.Warnings, warning) {
			t.Errorf("walking %v: warnings %q", walking, p.Warnings)
		}

		if slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.HasPrefix(w, "suite extra:") }) {
			t.Errorf("walking %v: extra_deps binds extra: %q", walking, p.Warnings)
		}

		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}

		var wp whyPlan
		if err := json.Unmarshal(data, &wp); err != nil {
			t.Fatal(err)
		}

		answers := explain(&wp, "", "web/src/page.test.ts")
		if len(answers) != 1 || !answers[0].Ran || len(answers[0].Reasons) != 1 || answers[0].Reasons[0].Text != affected.AloneReason {
			t.Errorf("walking %v: why: %+v", walking, answers)
		}
	}
}
