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
	"github.com/sideways-works/pipelinegen/plugin"
)

type fakeCheck struct {
	Name    string `json:"name"`
	Timeout string `json:"timeout"`
}

func (*fakeCheck) SettingNoun() string { return "a check" }

func fakeValidate(verb string, settings map[string]any) error {
	var s struct {
		Log    string      `json:"log"`
		Checks []fakeCheck `json:"checks"`
	}

	if err := plugin.DecodeSettings(settings, &s); err != nil {
		return err
	}

	if verb != plugin.CallRun && len(s.Checks) > 0 {
		return &plugin.SettingError{Place: "checks", Problem: "a run step's setting"}
	}

	return nil
}

func validatingRepo(t *testing.T, steps string) (*config.Config, string, string) {
	t.Helper()

	f := drivenRepo(t, steps, false)
	plugins := filepath.Join(f.root, "tools", "plugins")
	stubPath := filepath.Join(plugins, "stub")

	stub, err := os.ReadFile(stubPath)
	if err != nil {
		t.Fatal(err)
	}

	stub = []byte(strings.Replace(string(stub), "FAKE_DRIVER=1", "FAKE_DRIVER=1 FAKE_VALIDATES=1", 1))
	if err := os.WriteFile(stubPath, stub, 0o700); err != nil { //nolint:gosec // the test's own plugin
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

	return cfg, f.root, plugins
}

func problemsOf(findings []check.Finding) string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Kind+": "+f.Problem)
	}

	return strings.Join(out, "\n")
}

func TestCheckRefusesAMisspeltKeyInsideASetting(t *testing.T) {
	cfg, root, plugins := validatingRepo(t, `[{run: stub, with: {checks: [{name: a}, {name: b, timout: 5m}]}}]`)
	cache := t.TempDir()

	want := "bad-config: suites.e2e.steps[0].with.checks[1].timout: unknown key; the keys of a check are name, timeout"

	if got := problemsOf(stepsFindings(t.Context(), cfg, root, plugins, cache)); got != want {
		t.Errorf("findings:\n%s\nwant:\n%s", got, want)
	}

	if got := problemsOf(stepsFindings(t.Context(), cfg, root, t.TempDir(), cache)); got != want {
		t.Errorf("findings from the kept answer:\n%s\nwant:\n%s", got, want)
	}
}

func TestCheckTakesWhatAValidatingDriverTakes(t *testing.T) {
	cfg, root, plugins := validatingRepo(t, `[{setup: stub, with: {checks: [{name: a}]}}, {run: stub, with: {log: LOG, checks: [{name: a, timeout: 5m}]}}]`)

	want := "bad-config: suites.e2e.steps[0].with.checks: a run step's setting"
	if got := problemsOf(stepsFindings(t.Context(), cfg, root, plugins, t.TempDir())); got != want {
		t.Errorf("findings:\n%s\nwant:\n%s", got, want)
	}
}

func TestCheckSaysWhenItCantAskADriverToValidate(t *testing.T) {
	cfg, root, plugins := validatingRepo(t, `[{run: stub, with: {checks: [{name: a, timout: 5m}]}}]`)
	cache := t.TempDir()

	// describe is asked and kept; then the binary goes.
	if _, err := pinnedDescribe(t.Context(), cfg.Plugins[2], plugins, cache); err != nil {
		t.Fatal(err)
	}

	got := problemsOf(stepsFindings(t.Context(), cfg, root, t.TempDir(), cache))
	if !strings.Contains(got, "undescribed-driver: couldn't ask the driver stub to check its steps' settings") ||
		!strings.Contains(got, "suites.e2e.steps[0]") || strings.Contains(got, "timout") {
		t.Errorf("findings:\n%s", got)
	}
}
