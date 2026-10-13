package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// packages is an sbom that answers per package, as a Go sbom does: a set
// per directory under pkgs/, whose inputs are every file in it, with a
// unit per file. Asked about a path whose directory is gone, it answers
// that directory as an empty set.
func packages() map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindSBOM, Name: "texts", Namespaces: []string{"p"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Candidates:   []string{"pkgs/**"},
			Inputs:       map[string]string{plugin.CallUnits: plugin.PerSet, plugin.CallDetect: plugin.PerFile},
		}),
		plugin.CallDetect: func(in []byte) (any, error) {
			var req plugin.DetectRequest
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			var a plugin.DetectAnswer
			for _, p := range req.Paths {
				a.Claims = append(a.Claims, plugin.Claim{Path: p, Role: "source"})
			}

			return a, nil
		},
		plugin.CallUnits: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			paths := req.Paths
			if len(paths) == 0 {
				dirs, err := os.ReadDir(filepath.Join(req.Tree, "pkgs"))
				if err != nil {
					return nil, fmt.Errorf("packages: %w", err)
				}

				for _, d := range dirs {
					paths = append(paths, "pkgs/"+d.Name()+"/")
				}
			}

			var (
				a    plugin.UnitsAnswer
				seen = map[string]bool{}
			)

			for _, p := range paths {
				group := path.Dir(strings.TrimSuffix(p, "/") + "/x")
				if !strings.HasSuffix(p, "/") {
					group = path.Dir(p)
				}

				if seen[group] {
					continue
				}

				seen[group] = true

				set := plugin.Set{Group: group, Inputs: []string{}}

				files, _ := os.ReadDir(filepath.Join(req.Tree, filepath.FromSlash(group))) //nolint:errcheck // a directory that is gone is an empty set
				for _, f := range files {
					data, err := os.ReadFile(filepath.Join(req.Tree, filepath.FromSlash(group), f.Name()))
					if err != nil {
						return nil, fmt.Errorf("packages: %w", err)
					}

					sum := sha256.Sum256(data)

					set.Inputs = append(set.Inputs, group+"/"+f.Name())
					set.Units = append(set.Units, plugin.Unit{ID: "p:" + group + "#" + f.Name(), Digest: hex.EncodeToString(sum[:8])})
				}

				a.Sets = append(a.Sets, set)
			}

			return a, nil
		},
	}
}

func TestAPackageChangedTwoWaysKeepsItsUnits(t *testing.T) {
	s := newSetup(t, "observer")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, "texts"), []byte("#!/bin/sh\nFAKE=packages exec '"+exe+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	for f, body := range map[string]string{"why": "why 1\n", "outward": "outward 1\n", "report": "report 1\n", "tagged": "tagged\n"} {
		s.r.Write("pkgs/cli/"+f+".txt", body)
	}

	base := s.r.Commit("the package")

	plan := func(commit string) map[string]deps.Unit {
		t.Helper()

		tree, err := s.set(t).Tree(t.Context(), s.r.Dir, commit, 3)
		if err != nil {
			t.Fatal(err)
		}

		return tree.Units
	}

	s.r.Write("pkgs/cli/why.txt", "why 2\n")
	plan(s.r.Commit("why says it otherwise"))

	s.r.Git("checkout", "-q", base)
	s.r.Write("pkgs/cli/outward.txt", "outward 2\n")
	s.r.Git("rm", "-q", "pkgs/cli/tagged.txt")
	s.r.Write("pkgs/osproc/tagged.txt", "tagged\n")
	plan(s.r.Commit("outward changes, tagged moves out"))

	s.r.Write("pkgs/cli/why.txt", "why 2\n")
	both := s.r.Commit("why on the new base")
	warm := plan(both)

	s.cache.Dir = t.TempDir()
	cold := plan(both)

	if got, want := slices.Sorted(maps.Keys(warm)), slices.Sorted(maps.Keys(cold)); !slices.Equal(got, want) {
		t.Fatalf("units from the cache %v, cold %v", got, want)
	}
}
