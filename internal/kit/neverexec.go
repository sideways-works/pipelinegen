package kit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// neverExecCheck is the contract's line the kit checks as far as it can:
// a plugin parses the tree's files and never executes them.
const neverExecCheck = "the plugin parses the tree's files and never executes them"

const markerDir = "kit-markers"

// markerFiles are files a build, an interpreter or a config loader would
// run, each writing its name into marks if it does.
func markerFiles(marks string) map[string]string {
	at := func(name string) string { return strconv.Quote(filepath.Join(marks, name)) }
	touch := func(name string) string { return "touch " + filepath.Join(marks, name) }

	js := func(name string) string {
		return "import { writeFileSync } from \"node:fs\";\nwriteFileSync(" + at(name) + ", \"ran\");\nexport default {};\n"
	}

	names := []string{"build", "install", "postinstall", "preinstall", "prepare", "test"}
	scripts := make([]string, 0, len(names))

	for _, s := range names {
		scripts = append(scripts, strconv.Quote(s)+": "+strconv.Quote(touch("package.json-"+s)))
	}

	pkg := `{"name": "kit-markers", "version": "0.0.0", "private": true, "scripts": {` + strings.Join(scripts, ", ") + "}}\n"

	py := func(name string) string { return "open(" + at(name) + ", \"w\").write(\"ran\")\n" }

	return map[string]string{
		markerDir + "/vite.config.ts": js("vite.config.ts"),
		markerDir + "/vite.config.js": js("vite.config.js"),
		markerDir + "/package.json":   pkg,
		markerDir + "/gen.go":         "package markers\n\n//go:generate " + touch("go-generate") + "\n",
		markerDir + "/conftest.py":    py("conftest.py"),
		markerDir + "/setup.py":       py("setup.py"),
		markerDir + "/Makefile":       "all:\n\t" + touch("Makefile") + "\n",
	}
}

// neverExecutes runs the plugin's tree calls and observe on a fixture copy holding files that leave a
// marker when run; it proves only what these files would show.
func (k *kit) neverExecutes(ctx context.Context) {
	marks, tree := k.WorkDir(), k.WorkDir()

	if err := copyFixture(k.o.Tree, tree); err != nil {
		k.Check(neverExecCheck, "copying the fixture: %v", err)

		return
	}

	for name, body := range markerFiles(marks) {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			k.Check(neverExecCheck, "%v", err)

			return
		}

		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			k.Check(neverExecCheck, "%v", err)

			return
		}
	}

	fixture := k.o.Tree
	k.o.Tree = tree

	defer func() { k.o.Tree = fixture }()

	// what the calls answer is checked elsewhere: here only what they run
	claimed := k.detectQuietly(ctx)

	for _, call := range []string{plugin.CallUnits, plugin.CallLinks, plugin.CallBind} {
		if k.can(call) || call == plugin.CallUnits && k.desc.Kind == plugin.KindSBOM {
			_ = k.call(ctx, call, plugin.TreeRequest{Request: k.request(), Tree: tree, Claimed: claimed}, nil) //nolint:errcheck // only what it runs counts
		}
	}

	if k.can(plugin.CallObserve) {
		_ = k.observeCall(ctx, plugin.ObserveRequest{Request: k.request(), Tree: tree, Slice: "s", Dirs: []string{k.WorkDir(), k.WorkDir()}}, &plugin.ObserveAnswer{}) //nolint:errcheck // only what it runs counts
	}

	var ran []string

	entries, _ := os.ReadDir(marks) //nolint:errcheck // none is the answer wanted
	for _, e := range entries {
		ran = append(ran, e.Name())
	}

	k.Check(neverExecCheck, ifNot(len(ran) == 0, "it ran the fixture's "+strings.Join(ran, ", ")))
}

func (k *kit) detectQuietly(ctx context.Context) []plugin.Claim {
	if !k.can(plugin.CallDetect) && len(k.desc.Candidates) == 0 {
		return nil
	}

	work := k.WorkDir()

	var ans plugin.DetectAnswer
	if k.call(ctx, plugin.CallDetect, plugin.DetectRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Paths: k.candidates()}, &ans) != nil {
		return nil
	}

	claims, err := plugin.ReadLines(work, named(ans.Files, "claims"), ans.Claims)
	if err != nil {
		return nil
	}

	return claims
}

func copyFixture(from, to string) error {
	//nolint:wrapcheck // reported as the check's problem
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(from, p) //nolint:errcheck // p is under from

		if d.IsDir() {
			if d.Name() == gitDir {
				return filepath.SkipDir
			}

			return os.MkdirAll(filepath.Join(to, rel), 0o750)
		}

		data, err := os.ReadFile(p) //nolint:gosec // the fixture's own file
		if err != nil {
			return err //nolint:wrapcheck // reported as the check's problem
		}

		return os.WriteFile(filepath.Join(to, rel), data, 0o600) //nolint:gosec // rel is a path the walk found under the fixture
	})
}
