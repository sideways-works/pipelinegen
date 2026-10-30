package cli

import (
	"archive/zip"
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fill(t *testing.T, cache, rel string, n int, when time.Time) {
	t.Helper()

	p := filepath.Join(cache, filepath.FromSlash(rel))
	writeFile(t, p, strings.Repeat("x", n))

	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)

	return err == nil
}

func TestATrimKeepsWhatTheNextPlanUses(t *testing.T) {
	cache := t.TempDir()
	old, start := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Minute)

	const mb = 1 << 20

	fill(t, cache, "tree/README", mb, old)
	fill(t, cache, "rebuild/README", mb, old)
	fill(t, cache, "claims-1234/go.mod", mb, old)

	for i := range 8 {
		when := old.Add(time.Duration(i) * time.Minute)
		for _, ext := range []string{".json", ".hashes", ".edges"} {
			fill(t, cache, "snapshots/old"+string(rune('a'+i))+ext, 100, when)
		}
	}

	for _, ext := range []string{".json", ".hashes", ".edges"} {
		fill(t, cache, "snapshots/used"+ext, 100, old.Add(-time.Hour))
	}

	if err := os.Chtimes(filepath.Join(cache, "snapshots", "used.json"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	fill(t, cache, "snapshots/half.hashes.tmp", 100, old)

	fill(t, cache, "answers/aa/oldest", 3*mb, old)
	fill(t, cache, "answers/bb/newer", mb, old.Add(time.Hour))
	fill(t, cache, "plugins/go-sbom/state", 2*mb, old)
	fill(t, cache, "caches/gocache/x", 2*mb, old)
	fill(t, cache, "caches/gomodcache/mod/x", 2*mb, old)
	fill(t, cache, "store/abc/HEAD", mb, old)
	fill(t, cache, "bazel/root/install/x", 3*mb, old)
	fill(t, cache, "bazel/base/external/x", 3*mb, old)

	if err := os.Chmod(filepath.Join(cache, "caches", "gomodcache", "mod"), 0o500); err != nil { //nolint:gosec // a directory Go makes read-only
		t.Fatal(err)
	}

	// 17 MB remain after what always goes; dropping the oldest answer brings it under 15.
	got, err := trimCache(cache, 15*mb, start)
	if err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{"tree", "rebuild", "claims-1234", "snapshots/olda.json", "snapshots/oldd.edges", "snapshots/half.hashes.tmp",
		"answers/aa/oldest"} {
		if exists(filepath.Join(cache, gone)) {
			t.Errorf("%s stays", gone)
		}
	}

	for _, kept := range []string{"snapshots/used.json", "snapshots/used.hashes", "snapshots/olde.json", "snapshots/oldh.edges",
		"answers/bb/newer", "plugins/go-sbom/state", "caches/gocache/x"} {
		if !exists(filepath.Join(cache, kept)) {
			t.Errorf("%s is gone", kept)
		}
	}

	if got.snapshots != 4 {
		t.Errorf("dropped %d snapshots, want 4", got.snapshots)
	}

	var b bytes.Buffer

	got.say(&b, cache)

	if line := b.String(); !strings.HasPrefix(line, "pipelinegen: cache: kept ") || !strings.Contains(line, "bazel 6 MB") ||
		!strings.Contains(line, "dropped ") || !strings.Contains(line, "4 snapshots among them") {
		t.Errorf("said %q", line)
	}

	fill(t, cache, "tree/README", mb, old)

	if _, err := trimCache(cache, 0, start); err != nil {
		t.Fatal(err)
	}

	if exists(filepath.Join(cache, "tree")) || !exists(filepath.Join(cache, "answers", "bb", "newer")) {
		t.Error("with no cap, the tree stays or the answers go")
	}

	// At 10.5 MB the answers, the plugins' caches and the build cache go; the module cache, the store
	// and Bazel's output stay.
	if _, err := trimCache(cache, 21*mb/2, start); err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{"answers/bb/newer", "plugins", "caches/gocache"} {
		if exists(filepath.Join(cache, gone)) {
			t.Errorf("%s stays", gone)
		}
	}

	for _, kept := range []string{"caches/gomodcache/mod/x", "store/abc/HEAD", "bazel/root/install/x", "bazel/base/external/x"} {
		if !exists(filepath.Join(cache, kept)) {
			t.Errorf("%s is gone", kept)
		}
	}

	if _, err := trimCache(cache, mb, start); err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{"caches", "store", "bazel"} {
		if exists(filepath.Join(cache, gone)) {
			t.Errorf("%s stays", gone)
		}
	}
}

func TestATrimLeavesACacheInUse(t *testing.T) {
	cache := t.TempDir()
	fill(t, cache, "tree/README", 10, time.Now())

	unlock, ok, err := tryLockCache(cache)
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer unlock()

	got, err := trimCache(cache, 1, time.Now())
	if err != nil || !got.busy || !exists(filepath.Join(cache, "tree", "README")) {
		t.Fatalf("trimmed a cache in use: %+v %v", got, err)
	}
}

func TestATrimDropsWhatIsExtractedFromAnArchiveItKeeps(t *testing.T) {
	cache := t.TempDir()
	now := time.Now()

	const mb = 1 << 20

	for _, rel := range []string{
		"caches/gomodcache/github.com/o/a@v1.2.0/a.go",
		"caches/gomodcache/github.com/o/a@v1.2.0/sub/b.go",
		"caches/gomodcache/golang.org/x/mod@v0.30.0/go.mod",
	} {
		fill(t, cache, rel, mb, now)
	}

	for _, rel := range []string{
		"caches/gomodcache/cache/download/github.com/o/a/@v/v1.2.0.zip",
		"caches/gomodcache/cache/download/github.com/o/a/@v/v1.2.0.mod",
		"caches/gomodcache/cache/download/golang.org/x/mod/@v/v0.30.0.mod",
		"caches/gocache/aa/x-d",
		"bazel/root/cache/repos/v1/content_addressable/sha256/abc/file",
		"bazel/root/install/x",
		"bazel/base/external/x",
	} {
		fill(t, cache, rel, 100, now)
	}

	fill(t, cache, "bazel/root/cache/repos/v1/contents/abc/uuid/BUILD", 2*mb, now)

	for _, dir := range []string{"caches/gomodcache/github.com/o/a@v1.2.0/sub", "caches/gomodcache/github.com/o/a@v1.2.0"} {
		if err := os.Chmod(filepath.Join(cache, dir), 0o500); err != nil { //nolint:gosec // as Go leaves it
			t.Fatal(err)
		}
	}

	got, err := trimCache(cache, 0, now)
	if err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{"caches/gomodcache/github.com", "bazel/root/cache/repos/v1/contents"} {
		if exists(filepath.Join(cache, gone)) {
			t.Errorf("%s stays", gone)
		}
	}

	for _, kept := range []string{
		"caches/gomodcache/golang.org/x/mod@v0.30.0/go.mod",
		"caches/gomodcache/cache/download/github.com/o/a/@v/v1.2.0.zip",
		"caches/gocache/aa/x-d",
		"bazel/root/cache/repos/v1/content_addressable/sha256/abc/file",
		"bazel/root/install/x",
		"bazel/base/external/x",
	} {
		if !exists(filepath.Join(cache, kept)) {
			t.Errorf("%s is gone", kept)
		}
	}

	var b bytes.Buffer

	got.say(&b, cache)

	if line := b.String(); !strings.Contains(line, "dropped bazel repository contents 2 MB, gomodcache sources 2 MB") {
		t.Errorf("said %q", line)
	}

	fill(t, cache, "bazel/root/cache/repos/v2/contents/x", 100, now)

	if _, err := trimCache(cache, 0, now); err != nil || !exists(filepath.Join(cache, "bazel/root/cache/repos/v2/contents/x")) {
		t.Errorf("repositories without the archives went: %v", err)
	}
}

func TestGoReadsTheSameModulesAfterTheTrim(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}

	proxy, cache, work := t.TempDir(), t.TempDir(), t.TempDir()
	modcache := filepath.Join(cache, "caches", "gomodcache")

	at := filepath.Join(proxy, "example.com", "dep", "@v")
	writeFile(t, filepath.Join(at, "list"), "v1.0.0\n")
	writeFile(t, filepath.Join(at, "v1.0.0.info"), `{"Version":"v1.0.0"}`)
	writeFile(t, filepath.Join(at, "v1.0.0.mod"), "module example.com/dep\n\ngo 1.21\n")
	writeZip(t, filepath.Join(at, "v1.0.0.zip"), map[string]string{
		"example.com/dep@v1.0.0/go.mod":       "module example.com/dep\n\ngo 1.21\n",
		"example.com/dep@v1.0.0/dep.go":       "package dep\n\nfunc Answer() int { return 42 }\n",
		"example.com/dep@v1.0.0/inner/in.go":  "package inner\n\nconst Name = \"inner\"\n",
		"example.com/dep@v1.0.0/inner/README": "not Go\n",
	})

	writeFile(t, filepath.Join(work, "go.mod"), "module example.com/main\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n")
	writeFile(t, filepath.Join(work, "main.go"), "package main\n\nimport (\n\t\"example.com/dep\"\n\t\"example.com/dep/inner\"\n)\n\nfunc main() { println(dep.Answer(), inner.Name) }\n")

	list := func(goproxy string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-export", "-f", "{{.ImportPath}} {{.Dir}} {{.Export}}", "./...")
		cmd.Dir = work

		cmd.Env = append(os.Environ(), "GOMODCACHE="+modcache, "GOCACHE="+filepath.Join(cache, "caches", "gocache"),
			"GOPROXY="+goproxy, "GOFLAGS=-mod=mod", "GOSUMDB=off", "GONOSUMDB=", "GOPRIVATE=", "GOWORK=off", "GOTOOLCHAIN=local")

		var stderr bytes.Buffer

		cmd.Stderr = &stderr

		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list with GOPROXY=%s: %v\n%s", goproxy, err, stderr.String())
		}

		return string(out)
	}

	// Go leaves the extracted module read-only.
	t.Cleanup(func() { _ = removeAll(modcache) }) //nolint:errcheck // a test's directory

	before := list("file://" + filepath.ToSlash(proxy))
	if !strings.Contains(before, filepath.Join(modcache, "example.com", "dep@v1.0.0")) {
		t.Fatalf("the module wasn't extracted into the cache:\n%s", before)
	}

	got, err := trimCache(cache, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if exists(filepath.Join(modcache, "example.com", "dep@v1.0.0")) || got.dropped[moduleSources] == 0 {
		t.Fatalf("the extracted module stays: %v", got.dropped)
	}

	if after := list("off"); after != before {
		t.Errorf("Go reads other packages after the trim:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func writeZip(t *testing.T, p string, files map[string]string) {
	t.Helper()

	var b bytes.Buffer

	w := zip.NewWriter(&b)

	for _, name := range slices.Sorted(maps.Keys(files)) {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := f.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	writeFile(t, p, b.String())
}
