package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

const fillScript = `#!/bin/sh
echo "$(pwd)|${GITHUB_TOKEN:-no token}|${PIPELINEGEN_GIT_URL:-no pipelinegen}" >> "$GOMODCACHE/ran"
if [ -n "${FILL_FAILS:-}" ]; then echo "dial tcp: no network here" >&2; exit 1; fi
`

func fillSet(t *testing.T, fills bool) (*Set, string) {
	t.Helper()

	return fillSetWith(t, func(conf *config.Plugin) {
		if !fills {
			off := false
			conf.Fill = &off
		}
	})
}

func fillSetWith(t *testing.T, edit func(*config.Plugin)) (*Set, string) {
	t.Helper()

	bin := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "fakefill"), []byte(fillScript)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_TOKEN", "a-token-value-for-the-fill")
	t.Setenv("PIPELINEGEN_GIT_URL", "https://example.invalid")

	root := t.TempDir()
	for p, body := range map[string]string{"go.mod": "module a\n", "go.sum": "", "tools/go.mod": "module t\n", "README": "x\n"} {
		writeFillFile(t, filepath.Join(root, p), body)
	}

	gitFill(t, root, "init", "-q")
	gitFill(t, root, "add", "-A")
	gitFill(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "one")

	conf := config.Plugin{Name: "go-sbom", Kind: config.KindSBOM}
	edit(&conf)

	desc := plugin.Description{Kind: plugin.KindSBOM, Fill: map[string]plugin.Fill{
		"gomodcache": {Files: []string{"go.mod", "go.sum"}, Run: []string{"fakefill", "mod", "download"}},
	}}

	s, err := Load(t.Context(), Options{
		Config: &config.Config{Plugins: []config.Plugin{conf}}, Dir: t.TempDir(), Cache: t.TempDir(), Work: t.TempDir(),
		Described: map[string]plugin.Description{"go-sbom": desc},
	})
	if err != nil {
		t.Fatal(err)
	}

	return s, root
}

func writeFillFile(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitFill(t *testing.T, dir string, args ...string) {
	t.Helper()

	if out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func fillOnce(t *testing.T, s *Set, root string) ([]string, string) {
	t.Helper()

	files, err := blobs(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	var said strings.Builder

	s.fill(t.Context(), &Filler{Who: "affected", Out: &said}, RealPath(root), files)

	data, err := os.ReadFile(filepath.Join(s.o.Cache, "caches", "gomodcache", "ran"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	if len(data) == 0 {
		return nil, said.String()
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n"), said.String()
}

func TestAFillRunsOncePerContent(t *testing.T) {
	s, root := fillSet(t, true)

	ran, said := fillOnce(t, s, root)
	if len(ran) != 2 || !strings.HasSuffix(strings.Split(ran[0], "|")[0], filepath.Base(root)) || !strings.HasSuffix(strings.Split(ran[1], "|")[0], "tools") {
		t.Fatalf("ran: %v", ran)
	}

	for _, r := range ran {
		if !strings.HasSuffix(r, "|no token|no pipelinegen") {
			t.Errorf("the fill saw %s", r)
		}
	}

	if !strings.Contains(said, "pipelinegen affected: filling the gomodcache cache for go-sbom: fakefill mod download in tools, into ") {
		t.Errorf("said: %q", said)
	}

	if ran, said := fillOnce(t, s, root); len(ran) != 2 || said != "" {
		t.Fatalf("again: %v %q", ran, said)
	}

	writeFillFile(t, filepath.Join(root, "tools/go.mod"), "module t\n\nrequire x v1.0.0\n")
	gitFill(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "two")

	if ran, _ := fillOnce(t, s, root); len(ran) != 3 || !strings.Contains(ran[2], "tools|") {
		t.Fatalf("after a change to tools/go.mod: %v", ran)
	}
}

func TestAFailedFillSaysWhatItCannotTell(t *testing.T) {
	s, root := fillSet(t, true)
	t.Setenv("FILL_FAILS", "1")

	_, said := fillOnce(t, s, root)
	if !strings.Contains(said, "filling the gomodcache cache failed: `fakefill mod download` in .: ") || !strings.Contains(said, "dial tcp: no network here") ||
		!strings.Contains(said, "go-sbom may not tell what depends on what it lacks") ||
		!strings.Contains(said, "the fill runs without tokens from the environment: use a git credential helper or netrc, or run the command yourself") {
		t.Fatalf("said: %q", said)
	}

	if w := s.Report().Warnings; len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "filling the gomodcache cache failed") {
		t.Fatalf("warnings: %v", w)
	}

	t.Setenv("FILL_FAILS", "")

	if ran, _ := fillOnce(t, s, root); len(ran) != 4 {
		t.Fatalf("a failed fill isn't tried again: %v", ran)
	}
}

func TestAFillSkipsWhatItsReadersExclude(t *testing.T) {
	s, root := fillSetWith(t, func(conf *config.Plugin) { conf.Exclude = []string{"tools/**"} })

	ran, said := fillOnce(t, s, root)
	if len(ran) != 1 || strings.Contains(ran[0], "tools|") || strings.Contains(said, " in tools,") {
		t.Fatalf("ran %v, said %q", ran, said)
	}
}

func TestFillOff(t *testing.T) {
	s, root := fillSet(t, false)

	if ran, said := fillOnce(t, s, root); len(ran) != 0 || said != "" {
		t.Fatalf("fill: false ran %v %q", ran, said)
	}

	if fillerFrom(t.Context()) != nil {
		t.Fatal("a context without a filler has one")
	}
}

func TestFillProblem(t *testing.T) {
	ok := plugin.Fill{Files: []string{"go.mod"}, Run: []string{"go", "mod", "download"}}
	if p := FillProblem("gomodcache", ok); p != "" {
		t.Fatal(p)
	}

	for name, c := range map[string]struct {
		cache string
		f     plugin.Fill
	}{
		"a path":        {"gomodcache", plugin.Fill{Files: []string{"go.mod"}, Run: []string{"./download.sh"}}},
		"no program":    {"gomodcache", plugin.Fill{Files: []string{"go.mod"}}},
		"no files":      {"gomodcache", plugin.Fill{Run: []string{"go"}}},
		"a file path":   {"gomodcache", plugin.Fill{Files: []string{"sub/go.mod"}, Run: []string{"go"}}},
		"unknown cache": {"maven", plugin.Fill{Files: []string{"pom.xml"}, Run: []string{"mvn"}}},
	} {
		if FillProblem(c.cache, c.f) == "" {
			t.Errorf("%s passes", name)
		}
	}
}
