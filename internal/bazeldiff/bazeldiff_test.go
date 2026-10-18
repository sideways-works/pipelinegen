package bazeldiff

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestFetch(t *testing.T) {
	body := []byte("#!/bin/sh\necho fake\n")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		if r.URL.Path != "/v1.2.3/"+asset() {
			http.NotFound(w, r)

			return
		}

		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	rel := Release{Version: "v1.2.3", SHA256: config.Checksums{config.Platform(): good, "plan9-386": strings.Repeat("1", 64)}, URL: srv.URL + "/%s/%s"}

	tool, err := Fetch(t.Context(), srv.Client(), cache, rel)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(tool.Path)
	if err != nil || string(data) != string(body) {
		t.Fatalf("binary: %q %v", data, err)
	}

	if info, err := os.Stat(tool.Path); err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("not executable: %v", err)
	}

	if _, err := Fetch(t.Context(), srv.Client(), cache, rel); err != nil || hits.Load() != 1 {
		t.Fatalf("refetched: %d %v", hits.Load(), err)
	}

	bad := rel
	bad.SHA256 = config.Checksums{config.Platform(): strings.Repeat("0", 64)}

	if _, err := Fetch(t.Context(), srv.Client(), cache, bad); !errors.Is(err, ErrChecksum) {
		t.Fatalf("bad checksum: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cache, "bazel-diff", strings.Repeat("0", 64), "bazel-diff")); !os.IsNotExist(err) {
		t.Fatalf("bad binary kept: %v", err)
	}

	other := rel
	other.SHA256 = config.Checksums{"plan9-386": good}

	if _, err := Fetch(t.Context(), srv.Client(), t.TempDir(), other); !errors.Is(err, ErrBazelDiff) || !strings.Contains(err.Error(), "no checksum for "+config.Platform()) {
		t.Fatalf("no checksum for the platform: %v", err)
	}

	missing := rel
	missing.Version = "v9.9.9"

	if _, err := Fetch(t.Context(), srv.Client(), t.TempDir(), missing); err == nil {
		t.Fatal("a missing release fetched")
	}

	if _, err := Fetch(t.Context(), srv.Client(), cache, Release{Version: "v1"}); err == nil {
		t.Fatal("a release without a checksum fetched")
	}
}

func pinnedTool(t *testing.T) Tool {
	t.Helper()
	testrepo.NeedBazel(t)

	if path := os.Getenv("PIPELINEGEN_TEST_BAZEL_DIFF"); path != "" {
		return Tool{Path: path}
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}

	tool, err := Fetch(t.Context(), http.DefaultClient, filepath.Join(dir, "pipelinegen-test"), Pinned)
	if err != nil {
		t.Fatalf("fetch the pinned bazel-diff (or set PIPELINEGEN_TEST_BAZEL_DIFF): %v", err)
	}

	return tool
}

func TestToolOnFixture(t *testing.T) {
	tool := pinnedTool(t)
	r := testrepo.New(t)
	bz := testrepo.Bazel(t, r.Dir)
	out := t.TempDir()

	if err := tool.Hashes(t.Context(), bz, r.Dir, filepath.Join(out, "a.json"), filepath.Join(out, "a.edges")); err != nil {
		t.Fatal(err)
	}

	r.Write("api/orders/orders.go", "package orders // changed\n")

	if err := tool.Hashes(t.Context(), bz, r.Dir, filepath.Join(out, "b.json"), filepath.Join(out, "b.edges")); err != nil {
		t.Fatal(err)
	}

	got, err := tool.Impacted(t.Context(), bz, filepath.Join(out, "a.json"), filepath.Join(out, "b.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"//api/orders:orders", "//e2e:orders"} {
		if !slices.Contains(got, want) {
			t.Errorf("impacted %v lacks %s", got, want)
		}
	}

	for _, never := range []string{"//e2e:checkout", "//backendtests:money_test"} {
		if slices.Contains(got, never) {
			t.Errorf("impacted %v has %s", got, never)
		}
	}

	causes, err := tool.Explain(t.Context(), filepath.Join(out, "a.json"), filepath.Join(out, "b.json"), filepath.Join(out, "b.edges"), "//e2e:orders")
	if err != nil {
		t.Fatal(err)
	}

	if len(causes) == 0 || causes[0].Label != "//api/orders:orders" || causes[0].Path[len(causes[0].Path)-1] != "//e2e:orders" {
		t.Fatalf("causes %+v", causes)
	}
}

func TestTheAssetNamesMacOSAsTheReleaseDoes(t *testing.T) {
	for platform, want := range map[[2]string]string{
		{"darwin", "arm64"}: "bazel-diff-rust-macos-arm64",
		{"linux", "amd64"}:  "bazel-diff-rust-linux-amd64",
	} {
		if got := assetFor(platform[0], platform[1]); got != want {
			t.Errorf("%v: %q", platform, got)
		}
	}
}
