package affected

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestBazelHoldsAMacroDeclarationToWhatItReads(t *testing.T) {
	testrepo.NeedBazel(t)

	tools := t.TempDir()
	for p, text := range map[string]string{
		"REPO.bazel":         "",
		"macros/BUILD.bazel": "exports_files([\"web.bzl\"])\n",
		"macros/web.bzl": `def web_project(name, **kwargs):
    native.filegroup(name = name, srcs = native.glob(["src/**"]))
    native.filegroup(name = name + "_lint", srcs = native.glob(["lint/**"]))
`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tools, p)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(tools, p), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := testrepo.New(t)

	module, err := os.ReadFile(filepath.Join(r.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write("MODULE.bazel", string(module)+"\nlocal_repository(\n    name = \"web_tools\",\n    path = \""+tools+"\",\n)\n")
	r.Write("site/BUILD.bazel", "load(\"@web_tools//macros:web.bzl\", \"web_project\")\n\nweb_project(name = \"app\")\n")
	r.Write("site/src/main.ts", "export {};\n")
	r.Write("site/lint/rules.json", "{}\n")
	r.Write("site/notes.txt", "read by nobody\n")
	head := r.Commit("site")

	bz := testrepo.Bazel(t, r.Dir)

	const key = "@web_tools//macros:web.bzl%web_project"

	check := func(reads ...string) *MacroCheck {
		t.Helper()

		cfg := &config.Config{Macros: map[string]config.Macro{key: {Reads: reads, Reason: "builds the site"}}}

		m, err := checkMacros(t.Context(), Options{Config: cfg, Repo: gitx.Repo{Dir: r.Dir}}, bz, head)
		if err != nil {
			t.Fatal(err)
		}

		return m
	}

	if m := check("src/**", "lint/**"); len(m.Unconfirmed)+len(m.LeftOut) != 0 {
		t.Errorf("a true declaration: %+v", m)
	}

	m := check("src/**")
	if len(m.Unconfirmed) != 0 || len(m.LeftOut) != 1 || !strings.Contains(m.LeftOut[0], "//site:app reads site/lint/rules.json") {
		t.Errorf("a declaration leaving a file out: %+v", m)
	}

	m = check("src/**", "lint/**", "*.txt")
	if len(m.LeftOut) != 0 || len(m.Unconfirmed) != 1 || !strings.Contains(m.Unconfirmed[0], `reads "*.txt" claims site/notes.txt for //site:app`) {
		t.Errorf("a declaration claiming a file: %+v", m)
	}
}
