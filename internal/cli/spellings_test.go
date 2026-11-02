package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stderrOf(t *testing.T, f func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	os.Stderr = w

	f()

	os.Stderr = old

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestOldSpellingsAreSaidByCheckAndPlanAlone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), reportConfig)

	for _, spellings := range []bool{false, true} {
		c := common{root: root, spellings: spellings}

		said := stderrOf(t, func() {
			if _, err := c.load(); err != nil {
				t.Error(err)
			}
		})

		if got := strings.Contains(said, "suites.e2e.lock.minutes is the old spelling of suites.e2e.minutes"); got != spellings {
			t.Errorf("spellings %v: said %q", spellings, said)
		}
	}
}
