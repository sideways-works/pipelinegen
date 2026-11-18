package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestBothSpellingsOfARepositoryPinTheSameBuild(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sbom")

	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sum, err := FileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}

	list := []byte(sum + "  go-sbom-" + runtime.GOOS + "-" + runtime.GOARCH + "\n" + sum + "  go-sbom.mjs\n")
	if err := os.WriteFile(filepath.Join(dir, "sbom.SHA256SUMS"), list, 0o600); err != nil {
		t.Fatal(err)
	}

	listSum, err := FileSHA256(filepath.Join(dir, "sbom.SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	plugin := func(repository string) config.Plugin {
		return config.Plugin{Name: "sbom", Kind: config.KindSBOM, Repository: repository, Release: &config.Release{Version: "v1", Sums: listSum}}
	}

	var keys []string

	for _, repository := range []string{"o/go-sbom", "o/pipelinegen-sbom-go", "https://github.com/o/pipelinegen-sbom-go.git"} {
		p := plugin(repository)

		if got, err := PinnedSum(p, dir); err != nil || got != sum {
			t.Errorf("%s: pinned sum %q (%v), want %s", repository, got, err, sum)
		}

		built, np := build(p, dir, exe)
		if built != "" || np != nil {
			t.Errorf("%s: not the pinned build: %q %+v", repository, built, np)
		}

		k, err := (&Loaded{Conf: p, built: built}).key("units", "a.go").Hash()
		if err != nil {
			t.Fatal(err)
		}

		keys = append(keys, k)
	}

	if keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("the spellings key their answers apart: %v", keys)
	}

	for _, repository := range []string{"o/pipelinegen-go-sbom", "o/pipelinegen-correlator-go"} {
		if _, err := PinnedSum(plugin(repository), dir); !errors.Is(err, ErrNotListed) {
			t.Errorf("%s: %v, want %v", repository, err, ErrNotListed)
		}

		if _, np := build(plugin(repository), dir, exe); np == nil {
			t.Errorf("%s: said nothing", repository)
		}
	}
}
