package fetch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

const (
	release  = "/team/pipelinegen-driver-shell/releases/download/v0.2.0/"
	platform = "linux-arm64"
)

func (r *releases) pin() Pin {
	return Pin{Name: "shell", Repository: "team/pipelinegen-driver-shell", Version: "v0.2.0", URL: r.URL + "/%s/releases/download/%s/%s"}
}

func (r *releases) fetcher(t *testing.T) Fetcher {
	t.Helper()

	return Fetcher{Client: r.Client(), Cache: t.TempDir(), Under: "releases"}
}

func TestAFileThatMatchesItsPinIsKeptAndFetchedOnce(t *testing.T) {
	bin := []byte("the shell driver for linux-arm64")
	r := newReleases(t, map[string][]byte{release + "shell-driver-" + platform: bin})
	f := r.fetcher(t)

	pin := r.pin()
	pin.SHA256 = config.Checksums{platform: hexSum(bin)}

	got, err := f.Fetch(t.Context(), pin, platform)
	if err != nil {
		t.Fatal(err)
	}

	if data, err := os.ReadFile(got.Path); err != nil || string(data) != string(bin) || got.SHA256 != hexSum(bin) {
		t.Fatalf("%+v: %q %v", got, data, err)
	}

	if again, err := f.Fetch(t.Context(), pin, platform); err != nil || again.Path != got.Path || r.hits.Load() != 1 {
		t.Fatalf("again: %+v %v, %d downloads", again, err, r.hits.Load())
	}
}

func TestAFileThatDoesntMatchItsPinIsRefusedAndNotKept(t *testing.T) {
	r := newReleases(t, map[string][]byte{release + "shell-driver-" + platform: []byte("something else")})
	f := r.fetcher(t)

	pin := r.pin()
	pin.SHA256 = config.Checksums{platform: hexSum([]byte("the pinned one"))}

	if _, err := f.Fetch(t.Context(), pin, platform); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}

	if left, _ := filepath.Glob(filepath.Join(f.Cache, "releases", "*", "*")); len(left) != 0 { //nolint:errcheck // a valid pattern
		t.Fatalf("kept %v", left)
	}
}

// A pin by sums checks the release's SHA256SUMS by its checksum and the
// file by its line there, and keeps the list, so a second fetch needs no
// download at all.
func TestAPinBySumsChecksTheListAndThenTheFile(t *testing.T) {
	bin := []byte("pinned")
	sums := []byte(hexSum(bin) + "  shell-driver-" + platform + "\n" + hexSum([]byte("x")) + "  shell-driver-plan9-mips\n")
	r := newReleases(t, map[string][]byte{release + "shell-driver-" + platform: bin, release + "SHA256SUMS": sums})
	f := r.fetcher(t)

	pin := r.pin()
	pin.Sums = hexSum(sums)

	got, err := f.Fetch(t.Context(), pin, platform)
	if err != nil {
		t.Fatal(err)
	}

	if got.SHA256 != hexSum(bin) || string(got.Sums) != string(sums) {
		t.Fatalf("%+v", got)
	}

	if _, err := f.Fetch(t.Context(), pin, platform); err != nil || r.hits.Load() != 2 {
		t.Fatalf("again: %v, %d downloads", err, r.hits.Load())
	}

	pin.Sums = hexSum([]byte("another list"))
	if _, err := r.fetcher(t).Fetch(t.Context(), pin, platform); !errors.Is(err, ErrChecksum) {
		t.Fatalf("another list: %v", err)
	}

	pin.Sums = hexSum(sums)
	if _, err := r.fetcher(t).Fetch(t.Context(), pin, "darwin-arm64"); !errors.Is(err, ErrPin) {
		t.Fatalf("an unlisted platform: %v", err)
	}
}

// A Node plugin is released as one .mjs file: a pin by one sha256 is that
// file's checksum, on any platform.
func TestANodePluginIsItsScript(t *testing.T) {
	script := []byte("#!/usr/bin/env node\n")
	r := newReleases(t, map[string][]byte{release + "shell-driver.mjs": script})

	pin := r.pin()
	pin.SHA256 = config.Checksums{config.LinuxAMD64: hexSum(script)}
	pin.Script = hexSum(script)

	for _, p := range []string{config.LinuxAMD64, config.DarwinARM64} {
		if got, err := r.fetcher(t).Fetch(t.Context(), pin, p); err != nil || got.SHA256 != hexSum(script) {
			t.Fatalf("%s: %+v %v", p, got, err)
		}
	}
}

// A pin by one sha256 names the linux-amd64 file: on another platform no
// file can be checked by it, and nothing is downloaded.
func TestASHA256PinChecksNoOtherPlatform(t *testing.T) {
	r := newReleases(t, nil)

	pin := r.pin()
	pin.SHA256 = config.Checksums{config.LinuxAMD64: hexSum([]byte("x"))}

	if _, err := r.fetcher(t).Fetch(t.Context(), pin, config.DarwinARM64); !errors.Is(err, ErrPin) || r.hits.Load() != 0 {
		t.Fatalf("%v, %d downloads", err, r.hits.Load())
	}
}
