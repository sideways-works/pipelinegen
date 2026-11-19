package cli

import (
	"archive/zip"
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/container"
)

// The images the plugins' builds run in; a test whose image the machine lacks is skipped rather
// than pull hundreds of megabytes.
const (
	goBuildImage = "golang:1.25.7-bookworm@sha256:564e366a28ad1d70f460a2b97d1d299a562f08707eb0ecb24b659e5bd6c108e1"
	shBuildImage = "busybox:1.37@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"
)

func needBuildImage(t *testing.T, image string) {
	t.Helper()

	tool, err := container.Find("")
	if err != nil || !container.Reachable(t.Context(), tool) {
		t.Skipf("no container tool to build plugins with (docker or podman): %v", err)
	}

	if !container.HasImage(t.Context(), tool, image) {
		t.Skipf("%s doesn't have %s; pull it to run this test", tool, image)
	}
}

// stubSource is a Go program doing what the fixture's stub driver script does.
func stubSource(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	return `package main

import (
	"os"
	"syscall"
)

func main() {
	exe := ` + "`" + exe + "`" + `
	env := append(os.Environ(), "FAKE_DRIVER=1", "FAKE_MARKS="+os.Getenv("` + plugin.EnvMarks + `"), "FAKE_COMMIT="+os.Getenv("` + plugin.EnvCommit + `"), "FAKE_CHANGE="+os.Getenv("PIPELINEGEN_CHANGE"))
	panic(syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), env))
}
`
}

const hostPlatform = runtime.GOOS + "/" + runtime.GOARCH

// A driver pinned by commit is built from the manifest.yaml of its directory at that commit, in a
// container, once: the next run takes the build from the cache.
func TestADriverPinnedByCommitIsBuiltFromItsManifestOnce(t *testing.T) {
	needBuildImage(t, goBuildImage)

	f, _, _ := fetchingDriver(t, "path: driver/stub, commit: COMMIT")

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "go.mod"), "module example.test/plugins\n\ngo 1.25\n")
	writeFile(t, filepath.Join(src, "driver", "stub", "main.go"), stubSource(t))
	writeFile(t, filepath.Join(src, "driver", "stub", "manifest.yaml"), `name: stub
kind: driver
program: stub-driver
build:
  image: `+goBuildImage+`
  run: [go, build, -o, "{out}/stub-driver", .]
  env: {CGO_ENABLED: "0", GOOS: "{os}", GOARCH: "{arch}", GOTOOLCHAIN: local}
platforms: [`+hostPlatform+`]
`)
	gitT(t, src, "init", "-q", "-b", "main")
	gitT(t, src, "add", "-A")
	gitT(t, src, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "stub")

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	// git fetches the repository the config names from the local one
	repo := "https://plugins.example.test/o/pipelinegen-driver-stub"
	entry := strings.NewReplacer("COMMIT", gitT(t, src, "rev-parse", "HEAD")).Replace(string(cfg))
	entry = strings.Replace(entry, "repository: "+strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))+"/pipelinegen-driver-stub", "repository: "+repo, 1)
	writeFile(t, filepath.Join(f.root, config.FileName), entry)

	f.env["GIT_CONFIG_COUNT"], f.env["GIT_CONFIG_KEY_0"], f.env["GIT_CONFIG_VALUE_0"] = "1", "url."+src+".insteadOf", repo

	for i, builds := range []int{1, 0} {
		stdout, stderr, code := f.sliceRun(t)
		if code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
			t.Fatalf("run %d: slice run: %d %q\n%s", i, code, stdout, stderr)
		}

		if n := strings.Count(stderr, "building plugin stub for "+hostPlatform); n != builds {
			t.Fatalf("run %d built %d times, want %d:\n%s", i, n, builds, stderr)
		}
	}

	if _, err := os.Stat(f.driverLog); err != nil {
		t.Fatal("the built driver didn't run")
	}
}

// serveZips serves each zip by its path over TLS, and the file a child process trusts it by.
func serveZips(t *testing.T, zips map[string][]byte) (string, string) {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := zips[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(data) //nolint:errcheck // a test server
	}))
	t.Cleanup(srv.Close)

	ca := filepath.Join(t.TempDir(), "ca.pem")
	writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))

	return srv.URL, ca
}

const stubManifest = `# the stub driver, packed as a zip
name:   stub
kind: driver
program: stub-driver
build:
  image: ` + shBuildImage + `
  run: [sh, -c, "cp stub.sh {out}/stub-driver"]
platforms: [PLATFORM]
files: [LICENSE]
`

// plugin build packs the plugin as its manifest says, plugin add --zip pins that zip, and a run
// fetches, checks and runs it.
func TestABuiltZipIsPinnedAndRuns(t *testing.T) {
	needBuildImage(t, shBuildImage)

	f, stub, _ := fetchingDriver(t, "commit: 0123456789abcdef0123456789abcdef01234567")

	dir := t.TempDir()
	manifest := strings.Replace(stubManifest, "PLATFORM", hostPlatform, 1)
	writeFile(t, filepath.Join(dir, "manifest.yaml"), manifest)
	writeFile(t, filepath.Join(dir, "stub.sh"), string(stub))
	writeFile(t, filepath.Join(dir, "LICENSE"), "license\n")

	out := filepath.Join(t.TempDir(), "dist", "stub.zip")

	stdout, stderr, code := runIn(t, dir, f.env, "plugin", "build", dir, "--zip", out)
	if code != 0 {
		t.Fatalf("plugin build: %d %q\n%s", code, stdout, stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	sum := sumOf(data)
	if !strings.Contains(stdout, "sha256 "+sum) {
		t.Fatalf("plugin build says %q, not the zip's sha256 %s", stdout, sum)
	}

	if sums, _ := os.ReadFile(filepath.Join(filepath.Dir(out), "SHA256SUMS")); string(sums) != sum+"  stub.zip\n" { //nolint:errcheck // compared
		t.Fatalf("SHA256SUMS: %q", sums)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	var names []string

	for _, zf := range zr.File {
		names = append(names, zf.Name)

		if zf.Name == "manifest.yaml" {
			rc, err := zf.Open()
			if err != nil {
				t.Fatal(err)
			}

			var b bytes.Buffer

			_, _ = b.ReadFrom(rc) //nolint:errcheck // compared below
			_ = rc.Close()

			if b.String() != manifest {
				t.Fatalf("the zip's manifest isn't the source's byte for byte:\n%s", b.String())
			}
		}
	}

	if want := "LICENSE manifest.yaml stub-driver-" + config.Platform(); strings.Join(names, " ") != want {
		t.Fatalf("the zip holds %q, want %q", names, want)
	}

	base, ca := serveZips(t, map[string][]byte{"/stub.zip": data})
	f.env["SSL_CERT_FILE"] = ca

	if _, stderr, code := runIn(t, f.root, f.env, "plugin", "add", "--zip", base+"/stub.zip", "--sha256", strings.Repeat("0", 64)); code == 0 || !strings.Contains(stderr, "--sha256 says") {
		t.Fatalf("a wrong --sha256: %d\n%s", code, stderr)
	}

	// the name comes from the manifest, and the fixture has a stub already
	if _, stderr, code := runIn(t, f.root, f.env, "plugin", "add", "--zip", base+"/stub.zip"); code == 0 || !strings.Contains(stderr, "the config has a plugin named stub") {
		t.Fatalf("a taken name: %d\n%s", code, stderr)
	}

	stdout, stderr, code = runIn(t, f.root, f.env, "plugin", "add", "--zip", base+"/stub.zip", "--sha256", sum, "--name", "zipped")
	if code != 0 || !strings.Contains(stdout, "added zipped (driver) from "+base+"/stub.zip: sha256 "+sum) {
		t.Fatalf("plugin add: %d %q\n%s", code, stdout, stderr)
	}

	parsed, err := config.Load(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	if p, ok := parsed.Driver("zipped"); !ok || p.Zip == nil || p.Zip.SHA256 != sum || p.Zip.URL != base+"/stub.zip" || p.Repository != "" {
		t.Fatalf("the pin: %+v", p)
	}

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	if line := "  - name: zipped\n    kind: driver\n    zip: {url: '" + base + "/stub.zip', sha256: " + sum + "}\n"; !strings.Contains(string(cfg), line) {
		t.Fatalf("the config has no line %q:\n%s", line, cfg)
	}

	// the zip's entry takes the place of the fixture's stub

	var kept []string

	for l := range strings.SplitSeq(string(cfg), "\n") {
		if !strings.Contains(l, "name: stub,") {
			kept = append(kept, strings.Replace(l, "name: zipped", "name: stub", 1))
		}
	}

	writeFile(t, filepath.Join(f.root, config.FileName), strings.Join(kept, "\n"))

	if stdout, stderr, code := f.sliceRun(t); code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}

	kept1 := filepath.Join(f.env["XDG_CACHE_HOME"], "pipelinegen", "releases", sum, "manifest.yaml")
	if got, err := os.ReadFile(kept1); err != nil || string(got) != manifest { //nolint:gosec // the test's own cache
		t.Fatalf("the cache's manifest: %v", err)
	}
}

// A zip that isn't its pin is refused before it is unpacked, and the driver never runs.
func TestAZipThatIsNotItsPinNeverRuns(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "commit: 0123456789abcdef0123456789abcdef01234567")

	var z bytes.Buffer

	zw := zip.NewWriter(&z)
	for name, body := range map[string]string{"manifest.yaml": strings.Replace(stubManifest, "PLATFORM", hostPlatform, 1), "stub-driver-" + config.Platform(): string(stub)} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte(body)) //nolint:errcheck // a buffer
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	base, ca := serveZips(t, map[string][]byte{"/stub.zip": z.Bytes()})
	f.env["SSL_CERT_FILE"] = ca

	cfg, err := os.ReadFile(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	pinned := `zip: {url: "` + base + `/stub.zip", sha256: ` + strings.Repeat("0", 64) + `}`
	writeFile(t, filepath.Join(f.root, config.FileName), strings.Replace(string(cfg), "repository: "+repo+", commit: 0123456789abcdef0123456789abcdef01234567}", pinned+"}", 1))

	stdout, stderr, code := f.sliceRun(t)
	if code == 0 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}

	if _, err := os.Stat(f.driverLog); err == nil {
		t.Fatal("the driver ran")
	}

	if ents, _ := filepath.Glob(filepath.Join(f.env["XDG_CACHE_HOME"], "pipelinegen", "releases", "[0-9a-f]*")); len(ents) > 0 { //nolint:errcheck // a valid pattern
		t.Fatalf("the cache kept %v", ents)
	}
}

func TestAddRefusesAZipOverPlainHTTP(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.FileName), "pipeline: shop\nsuites: {}\n")

	var stderr bytes.Buffer
	if code := Run(t.Context(), []string{"plugin", "add", "--root", root, "--zip", "http://plugins.example/stub.zip"}, os.Stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "an https URL") {
		t.Fatalf("%d: %s", code, stderr.String())
	}
}
