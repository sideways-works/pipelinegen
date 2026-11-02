package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func stopBazelIn(t *testing.T, cfg *config.Config, dir, cache string) {
	t.Helper()

	tree := filepath.Join(cache, "tree", cfg.Bazel.Workspace)
	if _, err := os.Stat(tree); err != nil {
		return
	}

	if _, err := bazelFor(cfg, dir, cache)(tree).Run(context.WithoutCancel(t.Context()), "shutdown"); err != nil {
		t.Logf("bazel shutdown: %v", err)
	}
}

// A file Bazel extracts or downloads again is a new file, while one it only uses keeps its inode.
func inodes(t *testing.T, dir string, patterns ...string) map[string]uint64 {
	t.Helper()

	out := map[string]uint64{}

	for _, pattern := range patterns {
		paths, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range paths {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}

			if st, ok := info.Sys().(*syscall.Stat_t); ok && info.Mode().IsRegular() {
				rel, _ := filepath.Rel(dir, p) //nolint:errcheck // p is under dir
				out[rel] = st.Ino
			}
		}
	}

	return out
}

func installed(t *testing.T, dir string) map[string]uint64 {
	t.Helper()

	return inodes(t, filepath.Join(dir, "bazel"), "root/install/*/*")
}

func downloaded(t *testing.T, dir string) map[string]uint64 {
	t.Helper()

	return inodes(t, filepath.Join(dir, "bazel"), "root/cache/repos/v1/content_addressable/*/*/file")
}

func headHashes(t *testing.T, cache, commit string) string {
	t.Helper()

	metas, err := filepath.Glob(filepath.Join(cache, "snapshots", "*.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range metas {
		if strings.Contains(readFile(t, m), commit) {
			return readFile(t, strings.TrimSuffix(m, ".json")+".hashes")
		}
	}

	t.Fatalf("no snapshot of %s in %s", commit, cache)

	return ""
}

func sameFiles(t *testing.T, what string, before, after map[string]uint64) {
	t.Helper()

	for k, ino := range before {
		if got, ok := after[k]; !ok || got != ino {
			t.Errorf("%s: %s made again", what, k)
		}
	}

	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s: %s is new", what, k)
		}
	}
}

func TestAPlanStartsBazelWarmFromItsCache(t *testing.T) {
	testrepo.NeedBazel(t)

	bd := bazeldifftest.Tool(t).Path
	r := testrepo.New(t)

	conf := readFile(t, filepath.Join(r.Dir, config.FileName)) + "untested:\n  - {paths: [\"**\"], reason: the fixture's other files}\n"
	r.Write(config.FileName, conf)
	dependOnWhatTheyTest(r)
	first := r.Commit("first")

	r.Write("api/money/money.go", readFile(t, filepath.Join(r.Dir, "api", "money", "money.go"))+"\n// changed\n")
	second := r.Commit("second")

	r.Write("e2e/orders.spec.ts", readFile(t, filepath.Join(r.Dir, "e2e", "orders.spec.ts"))+"\n// changed\n")
	third := r.Commit("third")

	cfg := loadConfig(t, r.Dir)

	t.Setenv(envPipelineStep, "1")
	t.Setenv(envJobURL, "")
	t.Setenv("BAZELISK_HOME", "")

	builds := t.TempDir()
	dropped := map[string]int64{}

	plan := func(cache, base, head string) *affected.Plan {
		t.Helper()

		t.Cleanup(func() { stopBazelIn(t, cfg, r.Dir, cache) })

		p, err := planCommits(t.Context(), cfg, nil, r.Dir, "main", base, head, false, cache, bd, "")
		if err != nil {
			t.Fatalf("plan of %s: %v", short12(head), err)
		}

		return p
	}
	trim := func(cache string) {
		t.Helper()

		got := &trimmed{kept: map[string]int64{}, dropped: map[string]int64{}}
		if err := got.dropExtracted(cache); err != nil {
			t.Fatal(err)
		}

		for k, n := range got.dropped {
			dropped[k] += n
		}
	}

	build := func(cache, base, head string) *affected.Plan {
		t.Helper()

		p := plan(cache, base, head)

		stopBazelIn(t, cfg, r.Dir, cache)
		trim(cache)

		return p
	}

	one := filepath.Join(builds, "1", "plan-cache")
	build(one, first, second)

	if dropped[bazelRepoContent] == 0 {
		t.Fatalf("the step's trim dropped no extracted repository: %v", dropped)
	}

	install, downloads := installed(t, one), downloaded(t, one)
	if len(install) == 0 || len(downloads) == 0 {
		t.Fatalf("Bazel's installation and downloads aren't in the cache: %v %v", install, downloads)
	}

	// From here on nothing can be downloaded.
	for _, v := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		t.Setenv(v, "http://127.0.0.1:9")
	}

	build(one, second, third)
	sameFiles(t, "a build at the same path", install, installed(t, one))
	sameFiles(t, "a build at the same path", downloads, downloaded(t, one))

	two := filepath.Join(builds, "2", "plan-cache")
	if err := os.MkdirAll(filepath.Dir(two), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(one, two); err != nil {
		t.Fatal(err)
	}

	warm := build(two, first, third)
	sameFiles(t, "a build at another path", install, installed(t, two))
	sameFiles(t, "a build at another path", downloads, downloaded(t, two))

	noSnapshots := func() {
		t.Helper()

		if err := removeAll(filepath.Join(two, "snapshots")); err != nil {
			t.Fatal(err)
		}
	}

	noSnapshots()
	plan(two, second, third)
	trim(two)
	noSnapshots()

	again := build(two, first, third)
	sameFiles(t, "a second plan of the step", downloads, downloaded(t, two))

	for _, v := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		t.Setenv(v, "")
	}

	cold := filepath.Join(builds, "cold", "plan-cache")
	fresh := build(cold, first, third)

	if warm, fresh := headHashes(t, two, third), headHashes(t, cold, third); warm != fresh {
		t.Errorf("the warm plan's hashes of the head differ from a cold plan's")
	}

	for what, p := range map[string]*affected.Plan{"a warm build": warm, "a step's second plan": again} {
		if got, want := planSelection(p), planSelection(fresh); got != want {
			t.Errorf("%s selects %s, a cold plan %s", what, got, want)
		}
	}

	for _, dir := range []string{two, cold} {
		if err := removeAll(dir); err != nil {
			t.Error(err)
		}
	}
}

func planSelection(p *affected.Plan) string {
	out := make([]string, 0, len(p.Suites))
	for _, sp := range p.Suites {
		out = append(out, sp.Suite+": "+selectedNames(sp))
	}

	return strings.Join(out, "; ")
}
