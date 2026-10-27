package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

const fixture = "../../testdata/kit/pipelinegen.yaml"

func trustedOf(t *testing.T, text string) string {
	t.Helper()

	// each config from a directory of its own: where it lies is no part of it
	p := filepath.Join(t.TempDir(), "pipelinegen.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil { //nolint:gosec // a file in the test's own directory
		t.Fatal(err)
	}

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}

	s, err := From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return s.Trusted
}

func TestTheTrustedKeysAreTheSameBytesForTheSameConfig(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	blob := trustedOf(t, text)
	if blob == "" || trustedOf(t, text) != blob {
		t.Fatalf("two generations differ, or none: %q", blob)
	}

	untrusted := strings.NewReplacer(
		`tests: ["e2e/*.spec.ts"]`, `tests: ["e2e/**/*.spec.ts"]`,
		"parallel: 2", "parallel: 3",
	).Replace(text)
	if untrusted == text || trustedOf(t, untrusted) != blob {
		t.Error("a suite's tests or parallel changed the trusted keys")
	}

	trusted := strings.Replace(text, "repository: owner/go-sbom, commit: 0123456789abcdef0123456789abcdef01234567",
		"repository: owner/go-sbom, commit: 1123456789abcdef0123456789abcdef01234567", 1)
	if trusted == text || trustedOf(t, trusted) == blob {
		t.Error("a plugin's pin left the trusted keys as they were")
	}

	if strings.Contains(blob, "((") {
		t.Errorf("a credential reference in the trusted keys: %s", blob)
	}

	cfg, err := config.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}

	var keys config.Trusted
	if err := json.Unmarshal([]byte(blob), &keys); err != nil {
		t.Fatal(err)
	}

	if edited := cfg.WithTrusted(keys); len(edited) != 0 {
		t.Errorf("the config's own trusted keys read back as edits of %v", edited)
	}
}
