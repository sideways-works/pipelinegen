package plugins

import (
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
