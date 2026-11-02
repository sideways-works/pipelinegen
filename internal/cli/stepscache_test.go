package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestCheckAsksADriverOnAFreshMachine(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, with: {bogus: 1}}]", false)
	plugins := filepath.Join(f.root, "tools", "plugins")

	stub, err := os.ReadFile(filepath.Join(plugins, "stub"))
	if err != nil {
		t.Fatal(err)
	}

	pin := fmt.Sprintf("release: {version: v1, sha256: %x}", sha256.Sum256(stub))
	cfgPath := filepath.Join(f.root, config.FileName)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	text := strings.Replace(string(data), "{name: stub, kind: driver, repository: example/stub, commit: 0123456789abcdef0123456789abcdef01234567}",
		"{name: stub, kind: driver, repository: example/stub, "+pin+"}", 1)
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil { //nolint:gosec // the test's own repository
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	var problems []string
	for _, finding := range stepsFindings(t.Context(), cfg, f.root, plugins, filepath.Join(t.TempDir(), "never", "made")) {
		problems = append(problems, finding.Problem)
	}

	if all := strings.Join(problems, "\n"); !strings.Contains(all, "setting bogus is not one stub takes") {
		t.Errorf("the driver's settings went unchecked:\n%s", all)
	}
}

func TestCheckSaysWhenItCantAskADriver(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, with: {bogus: 1}}]", false)
	cfgPath := filepath.Join(f.root, config.FileName)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	text := strings.Replace(string(data), "{name: stub, kind: driver, repository: example/stub, commit: 0123456789abcdef0123456789abcdef01234567}",
		"{name: stub, kind: driver, repository: example/stub, release: {version: v1, sha256: "+strings.Repeat("ab", 32)+"}}", 1)
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil { //nolint:gosec // the test's own repository
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	var said []string

	for _, finding := range stepsFindings(t.Context(), cfg, f.root, t.TempDir(), t.TempDir()) {
		if finding.Kind != check.KindUndescribedDriver || !finding.Warning {
			t.Errorf("a finding other than the one expected: %+v", finding)

			continue
		}

		said = append(said, finding.Problem)
	}

	if len(said) != 1 || !strings.Contains(said[0], "couldn't ask the driver stub") || !strings.Contains(said[0], "suite e2e: steps[0] (run)") {
		t.Errorf("the check passed the step quietly, or said it oddly: %q", said)
	}
}
