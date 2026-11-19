package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

const stubCommitPin = "  - {name: stub, kind: driver, repository: example/stub, commit: 0123456789abcdef0123456789abcdef01234567}\n"

func sumOf(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// fetchingDriver is a driven repository whose plugins directory has no stub driver, pinned instead as
// pin says, a release the tests serve under a repository of the test's own.
func fetchingDriver(t *testing.T, pin string) (drivenFixture, []byte, string) {
	t.Helper()

	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)
	repo := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())) + "/pipelinegen-driver-stub"

	stub, err := os.ReadFile(filepath.Join(f.root, "tools", "plugins", "stub"))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(f.root, "tools", "plugins", "stub")); err != nil {
		t.Fatal(err)
	}

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	entry := "  - {name: stub, kind: driver, repository: " + repo + ", " + pin + "}\n"
	writeFile(t, filepath.Join(f.root, config.FileName), strings.Replace(string(cfg), stubCommitPin, entry, 1))

	f.env["XDG_CACHE_HOME"] = t.TempDir()

	return f, stub, repo
}

func (f drivenFixture) sliceRun(t *testing.T) (string, string, int) {
	t.Helper()

	return runIn(t, f.root, f.env, "slice", "run", "--suite", "e2e", "--root", f.root, "--slice", "e2e/cart.spec.ts")
}

// A driver the plugins directory lacks is fetched from its pinned release into the user cache,
// checked by its pin, and runs from there.
func TestADriverThePluginsDirectoryLacksIsFetchedByItsPin(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "release: {version: v1, sha256: "+sumOf(nil)+"}")

	stub = append(stub, []byte("# "+t.Name()+"\n")...)
	serveRelease(repo, "v1", "stub-driver-"+config.Platform(), stub)

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.root, config.FileName), strings.Replace(string(cfg), sumOf(nil), sumOf(stub), 1))

	stdout, stderr, code := f.sliceRun(t)
	if code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}

	kept := filepath.Join(f.env["XDG_CACHE_HOME"], "pipelinegen", "releases", sumOf(stub), "stub-driver")
	if data, err := os.ReadFile(kept); err != nil || string(data) != string(stub) {
		t.Fatalf("the cache's copy: %v", err)
	}
}

// A release whose file isn't the one its pin names is refused, and the driver never runs.
func TestAFetchedDriverThatIsNotItsPinNeverRuns(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "release: {version: v1, sha256: "+sumOf([]byte("the pinned build"))+"}")
	serveRelease(repo, "v1", "stub-driver-"+config.Platform(), stub)

	stdout, stderr, code := f.sliceRun(t)
	if code == 0 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}

	if _, err := os.Stat(f.driverLog); err == nil {
		t.Fatal("the driver ran")
	}
}

// A release pinned by sums is checked by its SHA256SUMS, kept beside the driver as the bootstrap keeps
// it, so the batch checks the driver against its pin again.
func TestADriverPinnedBySumsIsFetchedWithItsList(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "release: {version: v2, sums: SUMS}")

	sums := []byte(sumOf(stub) + "  stub-driver-" + config.Platform() + "\n" + sumOf([]byte("x")) + "  stub-driver-plan9-mips\n")
	serveRelease(repo, "v2", "SHA256SUMS", sums)
	serveRelease(repo, "v2", "stub-driver-"+config.Platform(), stub)

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.root, config.FileName), strings.Replace(string(cfg), "SUMS", sumOf(sums), 1))

	stdout, stderr, code := f.sliceRun(t)
	if code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}

	lists, _ := filepath.Glob(filepath.Join(f.env["XDG_CACHE_HOME"], "pipelinegen", "plugin-sets", "*", "*", "stub.SHA256SUMS")) //nolint:errcheck // a valid pattern
	if len(lists) != 1 {
		t.Fatalf("SHA256SUMS beside the driver: %v", lists)
	}
}

// A driver pinned by commit has no release to download: the error names it and how to build it.
func TestADriverPinnedByCommitIsBuiltNotFetched(t *testing.T) {
	f, _, _ := fetchingDriver(t, "commit: 0123456789abcdef0123456789abcdef01234567")
	// the repository can't be fetched, so its manifest can't be read: no network in a test
	f.env["GIT_ALLOW_PROTOCOL"] = "file"

	stdout, stderr, code := f.sliceRun(t)
	if code == 0 || !strings.Contains(stderr, "plugin stub is pinned by commit 0123456789ab, which has no release to download") ||
		!strings.Contains(stderr, "go build -trimpath -o "+filepath.Join(f.root, "tools", "plugins", "stub")) {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}
}

// A plugin of a repository holding several is fetched from its own release, tagged with its path.
func TestADriverOfAMonorepoIsFetchedByItsPathsTag(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "path: driver/stub, release: {version: v1, sha256: "+sumOf(nil)+"}")

	stub = append(stub, []byte("# "+t.Name()+"\n")...)

	serveRelease(repo, "v1", "stub-driver-"+config.Platform(), []byte("the repository's own release"))
	serveRelease(repo, "driver/stub/v1", "stub-driver-"+config.Platform(), stub)

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.root, config.FileName), strings.Replace(string(cfg), sumOf(nil), sumOf(stub), 1))

	stdout, stderr, code := f.sliceRun(t)
	if code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}
}

func TestABuildFromACommitOfAMonorepoBuildsInItsPath(t *testing.T) {
	err := commitPinned(config.Plugin{
		Name: "go", Kind: config.KindDriver, Repository: "git@github.com:o/plugins.git", Path: "driver/go",
		Commit: "0123456789abcdef0123456789abcdef01234567",
	}, "/tools", config.Platform())

	if err == nil || !strings.Contains(err.Error(), "git clone git@github.com:o/plugins.git go-driver && git -C go-driver checkout 0123456789abcdef0123456789abcdef01234567 && (cd go-driver/driver/go && CGO_ENABLED=0 go build -trimpath -o /tools/go ./cmd/go-driver)") {
		t.Fatal(err)
	}
}
