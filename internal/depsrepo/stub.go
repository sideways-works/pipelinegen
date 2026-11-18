package depsrepo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var selectorRef = regexp.MustCompile(`@pipelinegen_deps//` + slicesDir + `:([A-Za-z0-9_.+/-]+)`)

const dependentsSuffix = ".dependents"

// Stub binds every selector the tree's BUILD and .bzl files name, in both widths, to one stub file, so Bazel
// loads a fresh checkout before any plan wrote the real one; it writes no key, so no plan trusts it.
func Stub(root string) error {
	names := map[string]bool{}

	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if e.IsDir() {
			if p != root && skipDir(e.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if n := e.Name(); n != "BUILD" && n != "BUILD.bazel" && !strings.HasSuffix(n, ".bzl") {
			return nil
		}

		b, err := os.ReadFile(p) //nolint:gosec // the tree's own build files
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		for _, m := range selectorRef.FindAllSubmatch(b, -1) {
			names[strings.TrimSuffix(string(m[1]), dependentsSuffix)] = true
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("deps stub: %w", err)
	}

	const stubHeader = "# Written by pipelinegen for a fresh checkout: every slice bound to one stub file.\n\n"

	var build strings.Builder

	build.WriteString(stubHeader)
	build.WriteString("package(default_visibility = [\"//visibility:public\"])\n\n")
	build.WriteString("exports_files([\"stub\"])\n\n")

	for _, n := range slices.Sorted(keys(names)) {
		for _, sel := range []string{n, n + dependentsSuffix} {
			fmt.Fprintf(&build, "filegroup(name = %q, srcs = [\"stub\"])\n", sel)
		}
	}

	dir := filepath.Join(root, filepath.FromSlash(Dir))

	// a key left by an earlier plan no longer describes what is there
	if err := os.Remove(dir + keySuffix); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deps stub: %w", err)
	}

	return writeDir(dir, map[string][]byte{
		"REPO.bazel":               []byte(stubHeader),
		slicesDir + "/BUILD.bazel": []byte(build.String()),
		slicesDir + "/stub":        []byte("stub\n"),
	})
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "bazel-") ||
		name == "node_modules" || name == "testdata" || name == "vendor"
}
