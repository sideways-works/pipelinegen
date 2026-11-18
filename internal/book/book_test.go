package book

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func sameTree(t *testing.T, got, want string) {
	t.Helper()

	err := filepath.WalkDir(want, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(want, p)
		if err != nil {
			t.Fatal(err)
		}

		w, err := os.ReadFile(p) //nolint:gosec // the test's own golden file
		if err != nil {
			t.Fatal(err)
		}

		g, err := os.ReadFile(filepath.Join(got, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)

			return nil
		}

		if string(g) != string(w) {
			t.Errorf("%s:\n%q\nwant\n%q", rel, g, w)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, errGot := os.ReadDir(got)
	wants, errWant := os.ReadDir(want)

	if errGot != nil || errWant != nil {
		t.Fatal(errGot, errWant)
	}

	if len(entries) != len(wants) {
		t.Errorf("%d files, want %d", len(entries), len(wants))
	}
}

func TestTrim(t *testing.T) {
	out := t.TempDir()

	if err := Trim("testdata/trim/raw", out, "/scratch/w"); err != nil {
		t.Fatal(err)
	}

	sameTree(t, out, "testdata/trim/want")
}

func TestFill(t *testing.T) {
	docs := t.TempDir()
	if err := os.CopyFS(docs, os.DirFS("testdata/fill/docs")); err != nil {
		t.Fatal(err)
	}

	filled, err := Fill(docs, "testdata/fill/transcripts")
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(filled)

	if want := []string{filepath.Join(docs, "page.md"), filepath.Join(docs, "sub", "empty.md")}; !slices.Equal(filled, want) {
		t.Errorf("filled %v, want %v", filled, want)
	}

	sameTree(t, docs, "testdata/fill/want")

	filled, err = Fill(docs, "testdata/fill/transcripts", filepath.Join(docs, "page.md"))
	if err != nil || len(filled) != 0 {
		t.Errorf("filled again: %v %v", filled, err)
	}

	if err := os.WriteFile(filepath.Join(docs, "gone.md"), []byte("<!-- output: gone -->\n```\n```\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Fill(docs, "testdata/fill/transcripts", filepath.Join(docs, "gone.md")); err == nil {
		t.Error("a marker without its transcript filled")
	}
}

func pinnedConfig(files map[string][]byte) string {
	sum := func(p string) string { return sumOf(files[p]) }

	return `concourse:
  planner:
    bootstrap:
      release: {version: v0.1.13-rc.2, sha256: 564d3a72a9bc06fa09f08202f5563102dbef83cf4d6dafe4f29c998196ebd02f}
plugins:
  - repository: https://github.com/o/plugins
    with:
      - {name: go-sbom, kind: sbom, path: sbom/go, release: {version: v0.1.3-rc.3, sums: ` + sum("/p/go-sbom.SHA256SUMS") + `}}
      - name: go-correlator
        kind: correlator
        path: correlator/go
        release: {version: v0.1.3, sums: ` + sum("/p/go-correlator.SHA256SUMS") + `}
        config: {cover_dir: ctl}
  - name: concourse
    kind: runner
    release: {version: v0.1.7, sha256: ` + sum("/p/concourse") + `}
`
}

func TestPinProblems(t *testing.T) {
	files := map[string][]byte{
		"/p/go-sbom":                  []byte("sbom"),
		"/p/go-sbom.SHA256SUMS":       []byte(sumOf([]byte("sbom")) + "  go-sbom-linux-amd64\n"),
		"/p/go-correlator":            []byte("correlator"),
		"/p/go-correlator.SHA256SUMS": []byte(sumOf([]byte("correlator")) + "  go-correlator-linux-amd64\n"),
		"/p/concourse":                []byte("runner"),
	}
	read := func(p string) ([]byte, error) {
		if data, ok := files[p]; ok {
			return data, nil
		}

		return nil, os.ErrNotExist
	}
	config := pinnedConfig(files)

	if bad := PinProblems(config, "/p", "v0.1.13-rc.2", read); len(bad) != 0 {
		t.Errorf("the pinned releases: %v", bad)
	}

	correlatorPin := sumOf(files["/p/go-correlator.SHA256SUMS"])

	delete(files, "/p/concourse")
	files["/p/go-sbom"] = []byte("another build")
	files["/p/go-correlator.SHA256SUMS"] = []byte("another list")

	bad := PinProblems(config, "/p", "v0.1.13", read)
	want := []string{
		"pipelinegen on PATH is v0.1.13; example-notes pins v0.1.13-rc.2",
		"/p/go-sbom: " + sumOf([]byte("another build")) + ", which /p/go-sbom.SHA256SUMS doesn't list",
		"/p/go-correlator.SHA256SUMS: " + sumOf([]byte("another list")) + ", the pin is " + correlatorPin,
		"/p/concourse: missing, the pin is " + sumOf([]byte("runner")),
	}

	if !slices.Equal(bad, want) {
		t.Errorf("problems:\n%s\nwant\n%s", strings.Join(bad, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheGuideEdits(t *testing.T) {
	cfg := bindByRecording(firstConfig + coverage)

	for _, want := range []string{"    with:\n      - {name: go-sbom,", "    bind: recorded\n    correlators: [go-correlator]\n    steps:\n      - run: go\n",
		"correlators: [go-correlator, oapi-codegen-correlator]", "MODULE.bazel.lock, .bazelversion, package.json", "  - paths: [cmd/notes/BUILD.bazel]"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the config lacks %q:\n%s", want, cfg)
		}
	}

	if strings.Contains(cfg, "cover") {
		t.Errorf("step 6 links no cover package:\n%s", cfg)
	}

	f := firstTitleOnly()
	got := []string{f("        title: {type: string}"), f("        title: {type: string}")}

	if !slices.Equal(got, []string{"        title: {type: string, maxLength: 80}", "        title: {type: string}"}) {
		t.Errorf("titles %q", got)
	}

	if got := subRe(`      summary: Adds a note.`, "x")("      summary: Adds a note!"); got != "x" {
		t.Errorf("a summary %q", got)
	}

	if got := splitLines("a\r\nb\rc\n\nd"); !slices.Equal(got, []string{"a", "b", "c", "", "d"}) {
		t.Errorf("lines %q", got)
	}
}

func TestStepSevenEndsAtTheExample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipelinegen.yaml")
	if err := os.WriteFile(path, []byte(runTheServer(bindByRecording(firstConfig+coverage))), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	want, err := config.Load(filepath.Join("..", "..", "example-notes", "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range got.Plugins {
		i := slices.IndexFunc(want.Plugins, func(w config.Plugin) bool { return w.Name == p.Name })
		if i < 0 || !reflect.DeepEqual(p, want.Plugins[i]) {
			t.Errorf("plugin %s: the guide's %+v", p.Name, p)
		}
	}

	if len(got.Plugins) != len(want.Plugins)-1 {
		t.Errorf("the guide pins %d plugins; the example %d and its runner", len(got.Plugins), len(want.Plugins)-1)
	}

	for name, s := range got.Suites {
		w := want.Suites[name]
		w.Image = ""

		for i := range w.Steps {
			w.Steps[i].Image = ""
		}

		if !reflect.DeepEqual(s, w) {
			t.Errorf("suite %s:\nthe guide's %+v\nthe example's %+v", name, s, w)
		}
	}

	if !slices.Equal(got.Untested[len(got.Untested)-1].Paths, []string{"cmd/notes/cover.go"}) {
		t.Errorf("untested: %+v", got.Untested)
	}
}
