// Package gotests reads the names of the tests a Go test file declares, so
// a task can run one slice's tests by name from one compiled test binary.
package gotests

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Names are the top-level functions of a Go test file that `go test -run`
// matches: tests, fuzz targets and examples, sorted. TestMain runs for every
// test and is no test of its own; benchmarks only run with -bench.
func Names(path string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("gotests: %w", err)
	}

	var out []string

	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" {
			continue
		}

		for _, prefix := range []string{"Test", "Fuzz", "Example"} {
			if isTest(fn.Name.Name, prefix) {
				out = append(out, fn.Name.Name)
			}
		}
	}

	sort.Strings(out)

	return out, nil
}

// isTest is go test's own rule: the prefix, then nothing or a character
// that is not a lower-case letter (TestOrders, Test, but not Testify).
func isTest(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}

	if rest == "" {
		return true
	}

	r, _ := utf8.DecodeRuneInString(rest)

	return !unicode.IsLower(r)
}

// ByFile reads the names of every given file.
func ByFile(paths []string, read func(string) ([]byte, error)) (map[string][]string, error) {
	out := make(map[string][]string, len(paths))

	for _, p := range paths {
		src, err := read(p)
		if err != nil {
			return nil, fmt.Errorf("gotests: read %s: %w", p, err)
		}

		if out[p], err = Names(p, src); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// FileOf inverts ByFile's result: the file declaring each test, as
// durations.GoFiles takes it to turn `go test -json` times into file times.
func FileOf(byFile map[string][]string) map[string]string {
	out := map[string]string{}

	for file, names := range byFile {
		for _, n := range names {
			out[n] = file
		}
	}

	return out
}
