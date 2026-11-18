package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/forge/forgetest"
)

const pinsConfig = `pipeline: shop

suites:
  unit: {tag: slice-unit, run_slice: ci/unit.yml}

# the plugins, each pinned by its release
plugins:
  # what Go code exists
  - name: go-sbom
    kind: sbom
    repository: o/pipelinegen-sbom-go
    release: {version: v0.1.0, sha256: 1111111111111111111111111111111111111111111111111111111111111111}  # the first release
    config: {modules: [.]}

  - repository: https://github.com/o/plugins
    with:
      - {name: go-correlator, kind: correlator, path: correlator/go, release: {version: v0.1.0, sums: 2222222222222222222222222222222222222222222222222222222222222222}}

  - {name: stub, kind: driver, repository: o/stub, commit: 0123456789abcdef0123456789abcdef01234567}
`

// pinsHost is a GitHub with releases of every plugin the config names, and of more: drivers at
// several versions, a candidate among them, and a release GitHub marks as a prerelease.
func pinsHost(t *testing.T) *forgetest.GitHub {
	t.Helper()

	g := forgetest.Start(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	both := []string{"linux-amd64", "darwin-arm64"}

	for _, r := range []struct {
		project, tag, program, kind string
		pre                         bool
	}{
		{"o/pipelinegen-sbom-go", "v0.1.0", "go-sbom", "sbom", false},
		{"o/pipelinegen-sbom-go", "v0.2.0", "go-sbom", "sbom", false},
		{"o/pipelinegen-sbom-go", "v0.3.0-rc.1", "go-sbom", "sbom", false},
		{"o/plugins", "correlator/go/v0.1.0", "go-correlator", "correlator", false},
		{"o/plugins", "correlator/go/v0.1.1", "go-correlator", "correlator", false},
		{"o/plugins", "driver/go/v0.1.0", "go-driver", "driver", false},
		{"o/plugins", "driver/go/v0.2.0", "go-driver", "driver", false},
		{"o/plugins", "driver/go/v0.3.0-rc.1", "go-driver", "driver", false},
		{"o/plugins", "driver/go/v0.4.0", "go-driver", "driver", true},
		{"o/plugins", "sbom/ts/v1.0.0", "ts-sbom", "sbom", false},
		{"o/plugins", "v9.0.0", "plugins", "sbom", false},
		{"o/more", "correlator/react/v2.0.0", "react-correlator", "correlator", false},
		{"o/pipelinegen-driver-shell", "v1.2.0", "shell-driver", "driver", false},
	} {
		version := r.tag[strings.LastIndex(r.tag, "/")+1:]
		g.Release(r.project, r.tag, r.pre, forgetest.Plugin(r.program, r.program, r.kind, version, both...))
	}

	return g
}

func pinsRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.FileName), pinsConfig)

	return root
}

func pinsRun(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var out, errs bytes.Buffer

	code := Run(t.Context(), args, &out, &errs)

	return out.String(), errs.String(), code
}

// pinsGolden compares the edited config with testdata/pins/<name>.yaml; PINS_UPDATE=1
// writes it.
func pinsGolden(t *testing.T, root, name string) {
	t.Helper()

	got, err := os.ReadFile(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join("testdata", "pins", name+".yaml")

	if os.Getenv("PINS_UPDATE") != "" {
		writeFile(t, file, string(got))
	}

	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Fatalf("%s:\n%s\nwant:\n%s", name, got, want)
	}

	if _, err := config.Parse(got); err != nil {
		t.Fatalf("the edited config doesn't read: %v", err)
	}
}

func TestAddPinsEachPathsNewestReleaseIntoItsRepositorysGroup(t *testing.T) {
	g := pinsHost(t)
	root := pinsRepo(t)

	stdout, stderr, code := pinsRun(t, "plugin", "add", "o/plugins", "driver/go", "sbom/ts", "--root", root)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	want := "added go-driver (driver) from o/plugins at driver/go: v0.2.0, 2 files checked\n" +
		"added ts-sbom (sbom) from o/plugins at sbom/ts: v1.0.0, 2 files checked\n"
	if stdout != want {
		t.Fatalf("printed %q", stdout)
	}

	stdout, stderr, code = pinsRun(t, "plugin", "add", "--root", root, "git@github.com:o/more.git", "correlator/react")
	if code != 0 || !strings.HasPrefix(stdout, "added react-correlator (correlator) from git@github.com:o/more.git at correlator/react: v2.0.0") {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}

	pinsGolden(t, root, "added")

	asked := strings.Join(g.Asked(), "\n")
	for _, f := range []string{"driver/go/v0.2.0/go-driver-linux-amd64", "driver/go/v0.2.0/go-driver-darwin-arm64", "sbom/ts/v1.0.0/ts-sbom-darwin-arm64"} {
		if !strings.Contains(asked, "/o/plugins/releases/download/"+f) {
			t.Errorf("%s was not checked:\n%s", f, asked)
		}
	}
}

func TestAddPinsARepositoryPluginAsAnEntryOfItsOwn(t *testing.T) {
	pinsHost(t)
	root := pinsRepo(t)

	stdout, stderr, code := pinsRun(t, "plugin", "add", "https://github.com/o/pipelinegen-driver-shell", "--name", "shell", "--root", root)
	if code != 0 || stdout != "added shell (driver) from https://github.com/o/pipelinegen-driver-shell: v1.2.0, 2 files checked\n" {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}

	pinsGolden(t, root, "flat")
}

func TestAddTakesTheVersionAsked(t *testing.T) {
	pinsHost(t)
	root := pinsRepo(t)

	stdout, stderr, code := pinsRun(t, "plugin", "add", "https://github.com/o/plugins", "driver/go", "--version", "v0.3.0-rc.1", "--root", root)
	if code != 0 || !strings.Contains(stdout, ": v0.3.0-rc.1,") {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}

	_, stderr, code = pinsRun(t, "plugin", "add", "https://github.com/o/plugins", "sbom/ts", "--version", "v0.9.0", "--root", root)
	if code != 1 || !strings.Contains(stderr, "no release v0.9.0 (tagged sbom/ts/vX.Y.Z)") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// What can't be pinned leaves the config as it was.
func TestAddRefusesWhatItCantPin(t *testing.T) {
	g := pinsHost(t)

	files := forgetest.Plugin("lint-driver", "lint-driver", "driver", "v1.0.0", "linux-amd64")
	files["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  lint-driver-linux-amd64\n" + forgetest.Sum(files["plugin.json"]) + "  plugin.json\n")
	g.Release("o/plugins", "driver/lint/v1.0.0", false, files)

	for args, says := range map[string]string{
		"o/plugins correlator/go":              "the config has a plugin named go-correlator; `pipelinegen plugin upgrade go-correlator` moves its pin",
		"o/plugins driver/lint":                "checksum mismatch",
		"o/plugins driver/rust":                "no release tagged driver/rust/vX.Y.Z that is no prerelease",
		"o/plugins /driver/go":                 "a directory of the repository",
		"https://git.example.com/o/r x":        "a plain git server",
		"o/plugins driver/go sbom/ts --name x": "--name names one plugin",
	} {
		root := pinsRepo(t)

		_, stderr, code := pinsRun(t, append(append([]string{"plugin", "add"}, strings.Fields(args)...), "--root", root)...)
		if code == 0 || !strings.Contains(stderr, says) {
			t.Errorf("%s: exit %d: %s", args, code, stderr)
		}

		if got, err := os.ReadFile(filepath.Join(root, config.FileName)); err != nil || string(got) != pinsConfig {
			t.Errorf("%s: the config changed: %v", args, err)
		}
	}
}

func TestUpgradeMovesEveryReleasePinToTheNewestRelease(t *testing.T) {
	pinsHost(t)
	root := pinsRepo(t)

	stdout, stderr, code := pinsRun(t, "plugin", "upgrade", "--root", root)
	if code != 0 || stdout != "go-sbom: v0.1.0 -> v0.2.0\ngo-correlator: v0.1.0 -> v0.1.1\n" {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}

	pinsGolden(t, root, "upgraded")

	stdout, _, code = pinsRun(t, "plugin", "upgrade", "--root", root)
	if code != 0 || stdout != "go-sbom: v0.2.0, the newest\ngo-correlator: v0.1.1, the newest\n" {
		t.Fatalf("again: exit %d: %q", code, stdout)
	}

	stdout, stderr, code = pinsRun(t, "plugin", "upgrade", "go-sbom", "--pre", "--root", root)
	if code != 0 || stdout != "go-sbom: v0.2.0 -> v0.3.0-rc.1\n" {
		t.Fatalf("--pre: exit %d: %q %s", code, stdout, stderr)
	}

	pinsGolden(t, root, "upgraded-pre")
}

func TestUpgradeNamesWhatItCantMove(t *testing.T) {
	pinsHost(t)
	root := pinsRepo(t)

	for args, says := range map[string]string{
		"stub":  "plugin stub is pinned by commit",
		"other": "the config has no plugin other",
	} {
		_, stderr, code := pinsRun(t, append(append([]string{"plugin", "upgrade"}, strings.Fields(args)...), "--root", root)...)
		if code == 0 || !strings.Contains(stderr, says) {
			t.Errorf("%s: exit %d: %s", args, code, stderr)
		}
	}

	if got, err := os.ReadFile(filepath.Join(root, config.FileName)); err != nil || string(got) != pinsConfig {
		t.Fatalf("the config changed: %v", err)
	}
}

// A plugin whose releases can't be listed, or whose newest release fails its check, is said and
// left as it was; the others move, and the command fails at the end.
func TestUpgradeMovesWhatItCanAndNamesTheRest(t *testing.T) {
	g := pinsHost(t)

	files := forgetest.Plugin("go-correlator", "go-correlator", "correlator", "v0.1.2", "linux-amd64")
	files["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  go-correlator-linux-amd64\n")
	g.Release("o/plugins", "correlator/go/v0.1.2", false, files)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.FileName), pinsConfig+
		"  - {name: lint, kind: driver, repository: https://git.example.com/o/lint, release: {version: v1.0.0, sums: 3333333333333333333333333333333333333333333333333333333333333333}}\n")

	stdout, stderr, code := pinsRun(t, "plugin", "upgrade", "--root", root)
	if code != 1 || !strings.Contains(stderr, "2 of 3 plugins not moved: go-correlator, lint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 || lines[0] != "go-sbom: v0.1.0 -> v0.2.0" ||
		!strings.HasPrefix(lines[1], "go-correlator: not moved: ") || !strings.Contains(lines[1], "checksum mismatch") ||
		!strings.HasPrefix(lines[2], "lint: not moved: ") || !strings.Contains(lines[2], "a plain git server") {
		t.Fatalf("printed:\n%s", stdout)
	}

	pinsGolden(t, root, "upgraded-partly")
}
