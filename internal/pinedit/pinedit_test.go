package pinedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type release struct {
	Version string `yaml:"version"`
	Sums    string `yaml:"sums"`
}

func entry(name, kind, dir, version, sums string) []Field {
	return []Field{{"name", name}, {"kind", kind}, {"path", dir}, {"release", release{version, sums}}}
}

var sum4 = strings.Repeat("4", 64)

// golden compares what the edits made of fixture with testdata/<name>.golden; PINEDIT_UPDATE=1
// writes it.
func golden(t *testing.T, fixture, name string, edits func(*Doc) error) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}

	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if err := edits(d); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join("testdata", name+".golden")

	if os.Getenv("PINEDIT_UPDATE") != "" {
		if err := os.WriteFile(file, d.Bytes(), 0o600); err != nil { //nolint:gosec // a golden of the test's own
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	if string(d.Bytes()) != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", d.Bytes(), want)
	}
}

func TestAReleaseMovesInPlace(t *testing.T) {
	golden(t, "config.yaml", "moved", func(d *Doc) error {
		for _, m := range []struct{ name, version, key, sum string }{
			{"go-sbom", "v0.3.0", "sha256", strings.Repeat("a", 64)},
			{"go", "v0.3.0", "sums", strings.Repeat("b", 64)},
			{"go-correlator", "v0.3.0-rc.1", "sha256", strings.Repeat("c", 64)},
			{"lint", "v1.1.0", "sums", strings.Repeat("d", 64)},
		} {
			if err := d.SetRelease(m.name, m.version, m.key, m.sum); err != nil {
				return err
			}
		}

		return nil
	})
}

func TestAPinChangesItsKindOfChecksum(t *testing.T) {
	golden(t, "config.yaml", "resummed", func(d *Doc) error { return d.SetRelease("go-correlator", "v0.3.0", "sums", sum4) })
}

func TestAnEntryJoinsItsRepositorysGroup(t *testing.T) {
	golden(t, "config.yaml", "joined", func(d *Doc) error {
		// the same repository, spelled as GitHub's shorthand and as GitLab's https URL
		if err := d.AddToGroup("sideways-works/pipelinegen-plugins", entry("go-sbom2", "sbom", "sbom/go", "v0.2.1", sum4)); err != nil {
			return err
		}

		return d.AddToGroup("https://gitlab.com/group/tools", entry("vet", "driver", "driver/vet", "v1.0.0", sum4))
	})
}

func TestAGroupIsMadeForANewRepository(t *testing.T) {
	golden(t, "config.yaml", "grouped", func(d *Doc) error {
		if err := d.AddToGroup("https://github.com/o/more", entry("ts-sbom", "sbom", "sbom/ts", "v1.0.0", sum4)); err != nil {
			return err
		}

		return d.AddToGroup("https://github.com/o/more", entry("ts-correlator", "correlator", "correlator/ts", "v1.0.0", sum4))
	})
}

func TestARepositoryPluginIsAnEntryOfItsOwn(t *testing.T) {
	golden(t, "config.yaml", "flat", func(d *Doc) error {
		return d.AddFlat([]Field{{"name", "shell"}, {"kind", "driver"}, {"repository", "sideways-works/pipelinegen-driver-shell"}, {"release", release{"v0.2.1", sum4}}})
	})
}

func TestTheFirstPluginStartsTheList(t *testing.T) {
	golden(t, "noplugins.yaml", "first", func(d *Doc) error {
		return d.AddToGroup("https://github.com/o/plugins", entry("go", "driver", "driver/go", "v0.2.1", sum4))
	})

	golden(t, "emptyplugins.yaml", "firstempty", func(d *Doc) error {
		return d.AddToGroup("https://github.com/o/plugins", entry("go", "driver", "driver/go", "v0.2.1", sum4))
	})
}

func TestAnInlineListIsLeftAlone(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "inline.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.AddFlat(entry("y", "sbom", "", "v1", sum4)); err == nil || !strings.Contains(err.Error(), "plugins is written inline") {
		t.Fatal(err)
	}

	if err := d.SetRelease("x", "v2", "sums", sum4); err == nil || !strings.Contains(err.Error(), "plugin x has no release to move") {
		t.Fatal(err)
	}

	if string(d.Bytes()) != string(data) {
		t.Fatal("a refused edit changed the text")
	}
}
