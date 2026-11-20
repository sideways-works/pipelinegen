package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var (
	sumA = strings.Repeat("a", 64)
	sumB = strings.Repeat("b", 64)
)

func TestARepositoryIsAGitURLOrGitHubsShorthand(t *testing.T) {
	for _, repo := range []string{
		"o/r",
		"https://github.com/o/r",
		"https://github.com/o/r.git",
		"git@github.com:o/r.git",
		"ssh://git@gitlab.example.com:2222/group/sub/r.git",
		"https://gitlab.com/group/sub/r",
		"https://git.example.com/r",
	} {
		c, err := Parse([]byte(withRuns + "plugins:\n  - {name: x, kind: sbom, repository: \"" + repo + "\", release: {version: v1, sha256: " + sumA + "}}\n"))
		if err != nil {
			t.Errorf("%s: %v", repo, err)

			continue
		}

		if c.Plugins[0].Repository != repo {
			t.Errorf("%s is read as %s", repo, c.Plugins[0].Repository)
		}
	}

	for _, repo := range []string{"r", "o/r/x", "ftp://example.com/o/r", "https:///o/r"} {
		_, err := Parse([]byte(withRuns + "plugins:\n  - {name: x, kind: sbom, repository: \"" + repo + "\", release: {version: v1, sha256: " + sumA + "}}\n"))
		if err == nil || !strings.Contains(err.Error(), "an https or ssh URL to clone, or owner/name on GitHub") {
			t.Errorf("%s: %v", repo, err)
		}
	}
}

func TestAPathNamesAPluginOfAMonorepo(t *testing.T) {
	c, err := Parse([]byte(withRuns + "plugins:\n" +
		"  - {name: go, kind: driver, repository: https://github.com/o/plugins, path: driver/go, release: {version: v1, sha256: " + sumA + "}}\n" +
		"  - {name: shell, kind: driver, repository: o/pipelinegen-driver-shell, release: {version: v1, sha256: " + sumA + "}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got := []string{c.Plugins[0].Path, c.Plugins[0].Program(), c.Plugins[1].Path, c.Plugins[1].Program()}; !reflect.DeepEqual(got, []string{"driver/go", "go-driver", "", "shell-driver"}) {
		t.Fatalf("got %q", got)
	}

	if got := ProgramOf("o/r", "tools/lint/extra"); got != "extra-lint" {
		t.Fatalf("a deeper path names %s", got)
	}

	for dir, want := range map[string]string{
		"driver/go":               "go-driver",
		"sbom/go":                 "go-sbom",
		"correlator/oapi-codegen": "oapi-codegen-correlator",
		"runner/concourse":        "concourse-runner",
		"forge/github":            "github-forge",
		"":                        "pipelinegen-plugins",
	} {
		for _, repository := range []string{"sideways-works/pipelinegen-plugins", "https://github.com/sideways-works/pipelinegen-plugins.git"} {
			if got := ProgramOf(repository, dir); got != want {
				t.Errorf("%s at %q names %s, want %s", repository, dir, got, want)
			}
		}
	}

	for _, p := range []string{"/driver/go", "driver/go/", "../go", "driver/../go", "driver//go", "driver/.go"} {
		_, err := Parse([]byte(withRuns + "plugins:\n  - {name: x, kind: driver, repository: o/r, path: \"" + p + "\", release: {version: v1, sha256: " + sumA + "}}\n"))
		if err == nil || !strings.Contains(err.Error(), "a directory of the repository") {
			t.Errorf("%s: %v", p, err)
		}
	}
}

const grouped = `plugins:
  - repository: https://github.com/o/plugins
    with:
      - {name: go, kind: driver, path: driver/go, release: {version: v0.2.1, sha256: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `}}
      - name: go-sbom
        kind: sbom
        path: sbom/go
        release: {version: v0.2.1, sums: ` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `}
        config: {modules: [.]}
        exclude: [vendor/**]
  - {name: shell, kind: driver, repository: o/pipelinegen-driver-shell, release: {version: v0.2.1, sha256: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `}}
`

const flat = `plugins:
  - {name: go, kind: driver, repository: https://github.com/o/plugins, path: driver/go, release: {version: v0.2.1, sha256: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `}}
  - name: go-sbom
    kind: sbom
    repository: https://github.com/o/plugins
    path: sbom/go
    release: {version: v0.2.1, sums: ` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `}
    config: {modules: [.]}
    exclude: [vendor/**]
  - {name: shell, kind: driver, repository: o/pipelinegen-driver-shell, release: {version: v0.2.1, sha256: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `}}
`

func TestAGroupsEntriesTakeItsRepository(t *testing.T) {
	c, err := Parse([]byte(withRuns + grouped))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, p := range c.Plugins {
		got = append(got, p.Name+" "+p.Repository+" "+p.Path+" "+p.Release.Version)

		if p.With != nil {
			t.Errorf("%s is still a group", p.Name)
		}
	}

	want := []string{
		"go https://github.com/o/plugins driver/go v0.2.1",
		"go-sbom https://github.com/o/plugins sbom/go v0.2.1",
		"shell o/pipelinegen-driver-shell  v0.2.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}

	if sbom := c.Plugins[1]; !reflect.DeepEqual(sbom.Config, map[string]any{"modules": []any{"."}}) || !reflect.DeepEqual(sbom.Exclude, []string{"vendor/**"}) || sbom.Release.Sums != sumB {
		t.Fatalf("the entry's own keys: %+v", sbom)
	}
}

// A group is a spelling: the config it gives is the flat one's, for everything that reads it.
func TestAGroupReadsAsItsFlatSpelling(t *testing.T) {
	g, err := Parse([]byte(withRuns + grouped))
	if err != nil {
		t.Fatal(err)
	}

	f, err := Parse([]byte(withRuns + flat))
	if err != nil {
		t.Fatal(err)
	}

	for what, read := range map[string]func(*Config) any{
		"json":    func(c *Config) any { return c },
		"trusted": func(c *Config) any { return c.Trusted() },
	} {
		a, err := json.Marshal(read(g))
		if err != nil {
			t.Fatal(err)
		}

		b, err := json.Marshal(read(f))
		if err != nil {
			t.Fatal(err)
		}

		if string(a) != string(b) {
			t.Errorf("%s differs:\n%s\n%s", what, a, b)
		}
	}

	a, _ := yaml.Marshal(g) //nolint:errcheck // a config marshals
	b, _ := yaml.Marshal(f) //nolint:errcheck // a config marshals

	if string(a) != string(b) {
		t.Errorf("yaml differs:\n%s\n%s", a, b)
	}

	if m := MeaningOf(f, g, "pr"); m.Changed() {
		t.Errorf("regrouping means %+v", m.Parts)
	}

	h, err := ParseHistory([]byte(withRuns + grouped))
	if err != nil || len(h.Warnings) > 0 || !reflect.DeepEqual(h.Plugins, f.Plugins) {
		t.Fatalf("as history: %v %q %+v", err, h.Warnings, h.Plugins)
	}
}

func TestAMovedPathIsAnotherPin(t *testing.T) {
	f, err := Parse([]byte(withRuns + flat))
	if err != nil {
		t.Fatal(err)
	}

	moved, err := Parse([]byte(withRuns + strings.Replace(flat, "path: sbom/go", "path: sbom/golang", 1)))
	if err != nil {
		t.Fatal(err)
	}

	if samePin(f.Plugins[1], moved.Plugins[1]) {
		t.Fatal("another path is the same pin")
	}

	if reflect.DeepEqual(f.Trusted().Plugins, moved.Trusted().Plugins) {
		t.Fatal("the trusted keys don't see the path")
	}
}

func TestAGroupsMistakes(t *testing.T) {
	for bad, says := range map[string]string{
		"plugins:\n  - repository: o/plugins\n    with:\n      - {name: go, kind: driver, repository: o/other, path: driver/go, release: {version: v1, sha256: " + sumA + "}}\n": "plugin go: an entry under with takes its group's repository and names none of its own",
		"plugins:\n  - repository: o/plugins\n    release: {version: v1, sha256: " + sumA + "}\n    with:\n      - {name: go, kind: driver, path: driver/go}\n":                  "plugins[0] (o/plugins): a group takes repository and with only",
		"plugins:\n  - with:\n      - {name: go, kind: driver, path: driver/go, release: {version: v1, sha256: " + sumA + "}}\n":                                                 "plugins[0]: a group needs the repository its entries share",
		"plugins:\n  - {repository: o/plugins, with: []}\n":                              "plugins[0] (o/plugins): with lists no plugin",
		"plugins:\n  - repository: o/plugins\n    with:\n      - {name: go, with: []}\n": "plugin go: an entry under with is a plugin, not a group",
		"plugins:\n  - repository: o/plugins\n    with:\n      - {name: go, kind: driver, path: driver/go, release: {version: v1, sha256: " + sumA + "}}\n  - {name: go, kind: driver, repository: o/go, release: {version: v1, sha256: " + sumA + "}}\n": "plugins: every plugin needs a name of its own",
	} {
		_, err := Parse([]byte(withRuns + bad))
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestAGroupsKeysAreAPluginsKeys(t *testing.T) {
	keys := map[string]bool{}
	for _, k := range Keys() {
		keys[k.Path] = true
	}

	if !keys["plugins[].path"] || !keys["plugins[].with"] || keys["plugins[].with[].name"] {
		t.Fatalf("keys %v", keys)
	}
}
