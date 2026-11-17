package proof

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAFailedPlanSaysTheEndOfItsStderr(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plan.err")

	if err := os.WriteFile(p, []byte("warning: one\n\nwarning: two\npipelinegen affected: the config: no suite go\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, want := lastLines(p, 2), "\n  warning: two\n  pipelinegen affected: the config: no suite go"; got != want {
		t.Errorf("lastLines = %q, want %q", got, want)
	}

	if got := lastLines(filepath.Join(dir, "missing"), 3); got != "" {
		t.Errorf("a missing file says %q, want nothing", got)
	}
}
