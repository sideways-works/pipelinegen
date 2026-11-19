package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
)

func TestAPinReadsItsTreesConfigAsHistory(t *testing.T) {
	entry := config.Plugin{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Commit: "0123456789abcdef0123456789abcdef01234567"}
	s := &Set{Plugins: []*Loaded{{Conf: entry}}}

	pin := func(conf string) string {
		t.Helper()

		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}

		tree := &deps.Tree{Units: map[string]deps.Unit{}}
		s.pins(root, tree)

		return tree.Units[PinID("texts")].Digest
	}

	plugins := "suites:\n  unit: {tests: [\"*_test.go\"]}\nplugins:\n  - {name: texts, kind: sbom, repository: o/texts, commit: 0123456789abcdef0123456789abcdef01234567}\n"

	if _, err := config.Parse([]byte("retired_key: 1\n" + plugins)); err == nil {
		t.Fatal("the fixture's retired key loads strictly")
	}

	if got, want := pin("retired_key: 1\n"+plugins), pinDigest(entry); got != want {
		t.Errorf("a history-only config pins %q, want %q", got, want)
	}

	if got := pin("plugins: [\n"); got != "absent" {
		t.Errorf("an unreadable config pins %q", got)
	}
}

// A release's and a commit's pins digest as they did before zips were pinned, so no tree's plugin
// units change; a zip's pin digests by its url and checksum.
func TestPinDigestsKeepTheirValuesAndAZipHasItsOwn(t *testing.T) {
	release := config.Plugin{Name: "go", Kind: config.KindSBOM, Repository: "o/go", Release: &config.Release{Version: "v1", SHA256: "ab"}, Config: map[string]any{"modules": []any{"."}}}
	commit := config.Plugin{Name: "go", Kind: config.KindSBOM, Repository: "o/go", Commit: "0123456789abcdef0123456789abcdef01234567"}

	for p, text := range map[*config.Plugin]string{
		&release: "release v1 ab\ncommit \nkind sbom\n" + `{"modules":["."]}`,
		&commit:  "commit 0123456789abcdef0123456789abcdef01234567\nkind sbom\nnull",
	} {
		sum := sha256.Sum256([]byte(text))
		if got, want := pinDigest(*p), hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%+v digests %s, want %s", *p, got, want)
		}
	}

	zip := config.Plugin{Name: "go", Kind: config.KindSBOM, Zip: &config.Zip{URL: "https://p.example/go.zip", SHA256: "cd"}}
	other := zip
	other.Zip = &config.Zip{URL: "https://p.example/go.zip", SHA256: "ef"}

	if pinDigest(zip) == pinDigest(other) || pinDigest(zip) == pinDigest(config.Plugin{Name: "go", Kind: config.KindSBOM}) {
		t.Fatal("a zip's pin doesn't change its digest")
	}
}
