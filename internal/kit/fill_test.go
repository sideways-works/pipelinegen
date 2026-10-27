package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fillFakes is an sbom that declares how to fill its module cache: with a
// program that fills it once (fill), one that adds to it every time
// (fill-dirty), or a fill the core would never run (fill-bad).
func fillFakes(kind string) map[string]plugin.Handler {
	f := plugin.Fill{Files: []string{"go.mod"}, Run: []string{"kitfill"}}

	switch kind {
	case "fill-dirty":
		f.Run = []string{"kitfill-dirty"}
	case "fill-bad":
		f = plugin.Fill{Files: []string{"sub/go.mod"}, Run: []string{"./fill.sh"}}
	}

	return map[string]plugin.Handler{plugin.CallDescribe: describe(plugin.Description{
		Kind: plugin.KindSBOM, Namespaces: []string{"t"}, Candidates: []string{"**/go.mod"},
		Caches: []string{"gomodcache"}, Fill: map[string]plugin.Fill{"gomodcache": f},
	})}
}

func runFill(t *testing.T, kind string) map[string]string {
	t.Helper()

	bin := t.TempDir()
	for name, body := range map[string]string{
		"kitfill":       "#!/bin/sh\nd=\"$GOMODCACHE/m/$(basename \"$(pwd)\")\"\n[ -d \"$d\" ] || { mkdir -p \"$d\" && echo v1 > \"$d/v\" && chmod a-w \"$d/v\" \"$d\"; }\n",
		"kitfill-dirty": "#!/bin/sh\necho more >> \"$GOMODCACHE/log\"\n",
	} {
		if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, name), []byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	tree := t.TempDir()
	for _, p := range []string{"go.mod", "tools/go.mod"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tree, p)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(tree, p), []byte("module m\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out := map[string]string{}
	work := t.TempDir()

	for _, r := range Run(t.Context(), Options{Path: fakeExe(t, kind), Tree: tree, Work: work}) {
		out[r.Check] = r.Problem
	}

	// a fill leaves its cache read-only, as Go's module cache is; the kit
	// takes it away again
	if entries, err := os.ReadDir(filepath.Join(work, "fill")); err == nil && len(entries) > 0 {
		t.Errorf("the kit left the fill's cache behind: %v", entries)
	}

	return out
}

func TestTheKitChecksAFill(t *testing.T) {
	got := runFill(t, "fill")
	if p, ok := got["fill is declared as the core runs it"]; !ok || p != "" {
		t.Errorf("declared: %v %q", ok, p)
	}

	if p, ok := got["a second fill of gomodcache changes nothing"]; !ok || p != "" {
		t.Errorf("twice: %v %q", ok, p)
	}

	if p := runFill(t, "fill-dirty")["a second fill of gomodcache changes nothing"]; !strings.Contains(p, "the second run changed log") {
		t.Errorf("dirty: %q", p)
	}

	bad := runFill(t, "fill-bad")
	if p := bad["fill is declared as the core runs it"]; !strings.Contains(p, "never a path") {
		t.Errorf("bad: %q", p)
	}

	if _, ran := bad["a second fill of gomodcache changes nothing"]; ran {
		t.Error("a fill the core would refuse ran")
	}
}
