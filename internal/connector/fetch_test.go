package connector

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

type releases struct {
	*httptest.Server

	files map[string][]byte
	hits  atomic.Int32
}

func newReleases(t *testing.T, files map[string][]byte) *releases {
	t.Helper()

	r := &releases{files: files}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)

		data, ok := r.files[req.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write(data) //nolint:errcheck // a test server
	}))
	t.Cleanup(r.Close)

	return r
}

const binaryPath = "/team/pipelinegen-connector-github/releases/download/v0.1.0/github-connector-" + runtime.GOOS + "-" + runtime.GOARCH

func pinOf(r *releases, sum string) config.ConnectorPin {
	return config.ConnectorPin{
		Name: "github", Repository: "team/pipelinegen-connector-github", Version: "v0.1.0",
		SHA256: config.Checksums{config.Platform(): sum}, URL: r.URL + "/%s/releases/download/%s/%s",
	}
}

func TestFetchChecksAndKeepsTheBinary(t *testing.T) {
	bin := []byte("#!/bin/sh\necho connector\n")
	r := newReleases(t, map[string][]byte{binaryPath: bin})
	cache := t.TempDir()

	got, err := Fetch(t.Context(), r.Client(), "", cache, pinOf(r, hexSum(bin)))
	if err != nil {
		t.Fatal(err)
	}

	if data, err := os.ReadFile(got); err != nil || string(data) != string(bin) {
		t.Fatalf("%s: %q %v", got, data, err)
	}

	if again, err := Fetch(t.Context(), r.Client(), "", cache, pinOf(r, hexSum(bin))); err != nil || again != got || r.hits.Load() != 1 {
		t.Fatalf("again: %s %v, %d downloads", again, err, r.hits.Load())
	}
}

func TestFetchRefusesAnotherBinary(t *testing.T) {
	r := newReleases(t, map[string][]byte{binaryPath: []byte("something else")})
	cache := t.TempDir()

	if _, err := Fetch(t.Context(), r.Client(), "", cache, pinOf(r, hexSum([]byte("the pinned one")))); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}

	if left, _ := filepath.Glob(filepath.Join(cache, "connectors", "*", "*")); len(left) != 0 { //nolint:errcheck // a valid pattern
		t.Fatalf("kept %v", left)
	}
}

func TestFetchRefusesAPinWithoutAChecksum(t *testing.T) {
	r := newReleases(t, nil)

	for _, pin := range []config.ConnectorPin{pinOf(r, "TODO"), pinOf(r, "")} {
		if _, err := Fetch(t.Context(), r.Client(), "", t.TempDir(), pin); !errors.Is(err, ErrPin) || r.hits.Load() != 0 {
			t.Fatalf("%+v: %v, %d downloads", pin, err, r.hits.Load())
		}
	}
}

// A pipeline's bootstrap puts the pinned binary in the plugins directory:
// it runs from there, and a different one there is passed over.
func TestFetchTakesTheBootstrapsCopy(t *testing.T) {
	bin := []byte("pinned")
	r := newReleases(t, map[string][]byte{binaryPath: bin})
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "github"), bin, 0o700); err != nil {
		t.Fatal(err)
	}

	if got, err := Fetch(t.Context(), r.Client(), dir, t.TempDir(), pinOf(r, hexSum(bin))); err != nil || got != filepath.Join(dir, "github") || r.hits.Load() != 0 {
		t.Fatalf("%s %v, %d downloads", got, err, r.hits.Load())
	}

	if err := os.WriteFile(filepath.Join(dir, "github"), []byte("another"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got, err := Fetch(t.Context(), r.Client(), dir, t.TempDir(), pinOf(r, hexSum(bin))); err != nil || got == filepath.Join(dir, "github") {
		t.Fatalf("%s %v", got, err)
	}
}

func TestFetchBySums(t *testing.T) {
	bin := []byte("pinned")
	sums := []byte(hexSum(bin) + "  github-connector-" + runtime.GOOS + "-" + runtime.GOARCH + "\n" + hexSum([]byte("x")) + "  github-connector-plan9-mips\n")
	r := newReleases(t, map[string][]byte{binaryPath: bin, "/team/pipelinegen-connector-github/releases/download/v0.1.0/SHA256SUMS": sums})

	pin := pinOf(r, "")
	pin.SHA256, pin.Sums = nil, hexSum(sums)

	if _, err := Fetch(t.Context(), r.Client(), "", t.TempDir(), pin); err != nil {
		t.Fatal(err)
	}

	pin.Sums = hexSum([]byte("another list"))

	if _, err := Fetch(t.Context(), r.Client(), "", t.TempDir(), pin); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
}
