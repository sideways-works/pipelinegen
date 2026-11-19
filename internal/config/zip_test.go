package config

import (
	"slices"
	"strings"
	"testing"
)

var zipSum = strings.Repeat("c", 64)

func TestAPluginPinnedByAZipNeedsNoRepository(t *testing.T) {
	c, err := Parse([]byte(withRuns + "plugins:\n  - {name: lint, kind: driver, zip: {url: \"https://plugins.example/lint.zip\", sha256: " + zipSum + "}}\n" +
		"  - {name: go, kind: sbom, repository: o/plugins, path: sbom/go, zip: {url: \"https://plugins.example/go.zip\", sha256: " + zipSum + "}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if z := c.Plugins[0].Zip; z == nil || z.URL != "https://plugins.example/lint.zip" || z.SHA256 != zipSum || c.Plugins[0].Release != nil {
		t.Fatalf("zip pin %+v", c.Plugins[0])
	}
}

func TestZipPinsAreRefused(t *testing.T) {
	zip := `zip: {url: "https://plugins.example/x.zip", sha256: ` + zipSum + `}`

	for bad, says := range map[string]string{
		"{name: x, kind: sbom, repository: o/x, commit: " + strings.Repeat("0", 40) + ", " + zip + "}":                "exactly one of release, commit or zip",
		"{name: x, kind: sbom, repository: o/x, release: {version: v1, sha256: " + zipSum + "}, " + zip + "}":         "exactly one of release, commit or zip",
		`{name: x, kind: sbom, zip: {url: "http://plugins.example/x.zip", sha256: ` + zipSum + `}}`:                   "an https url",
		`{name: x, kind: sbom, zip: {url: "https://plugins.example/x.zip", sha256: ` + strings.ToUpper(zipSum) + `}}`: "64 lower-case hex digits",
		`{name: x, kind: sbom, zip: {url: "https://plugins.example/x.zip"}}`:                                          "64 lower-case hex digits",
		`{name: x, kind: sbom, zip: {url: "https://plugins.example/x.zip", sha256: ` + zipSum + `, size: 3}}`:         "field size not found",
		`{name: github, kind: connector, zip: {url: "https://plugins.example/x.zip", sha256: ` + zipSum + `}}`:        "a connector entry needs a release",
		"{name: x, kind: sbom, repository: o/x}":                                                                      "release (version and one of sha256 or sums), commit (40 hex) or zip (url and sha256)",
	} {
		_, err := Parse([]byte(withRuns + "plugins:\n  - " + bad + "\n"))
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: got %v, want %q", bad, err, says)
		}
	}
}

func TestAChangesZipPinIsUndoneAsAReleasePinIs(t *testing.T) {
	pinned := trustedBase + `  - {name: lint, kind: driver, zip: {url: "https://plugins.example/lint.zip", sha256: ` + zipSum + `}}` + "\n"
	trusted := parseTrusted(t, pinned).Trusted()

	if same := parseTrusted(t, pinned); len(same.WithTrusted(trusted)) != 0 {
		t.Fatal("the same zip pin is an edit")
	}

	c := parseTrusted(t, strings.Replace(pinned, "https://plugins.example/lint.zip", "https://evil.example/lint.zip", 1))
	if edited := c.WithTrusted(trusted); !slices.Contains(edited, "plugins") {
		t.Fatalf("edited %v", edited)
	}

	if z := c.Plugins[2].Zip; z == nil || z.URL != "https://plugins.example/lint.zip" {
		t.Fatalf("the change's zip stands: %+v", c.Plugins[2])
	}
}

func TestAZipPinsChangeIsADriversChange(t *testing.T) {
	a := Plugin{Name: "lint", Kind: KindDriver, Zip: &Zip{URL: "https://p.example/a.zip", SHA256: zipSum}}
	b := a
	b.Zip = &Zip{URL: "https://p.example/a.zip", SHA256: strings.Repeat("d", 64)}

	if !samePin(a, a) || samePin(a, b) {
		t.Fatal("samePin doesn't tell zips apart")
	}
}
