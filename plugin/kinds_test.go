package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyPipelinegensKindsAreItsOwn(t *testing.T) {
	for _, kind := range []string{KindSBOM, KindCorrelator, KindRunner, KindDriver, KindForge} {
		if err := CheckKind(kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}

	if err := CheckKind("connector"); !errors.Is(err, ErrKind) || !strings.Contains(err.Error(), "kind connector is not a pipelinegen plugin kind; pipelinegen's is forge") {
		t.Fatal(err)
	}

	// the kinds of another system's plugins, which pipelinegen never runs
	for _, kind := range []string{"step", "trigger", "notifier", "extension", "service", "signin", "meta", ""} {
		if err := CheckKind(kind); !errors.Is(err, ErrKind) || !strings.Contains(err.Error(), "pipelinegen's are sbom, correlator, runner, driver, forge") {
			t.Errorf("%q: %v", kind, err)
		}
	}
}

func TestADescribeAnswerOfAnotherKindIsRefused(t *testing.T) {
	for kind, says := range map[string]string{
		"connector": "kind connector is not a pipelinegen plugin kind; pipelinegen's is forge",
		"step":      `kind "step" is not a pipelinegen plugin kind`,
	} {
		path := filepath.Join(t.TempDir(), "other")

		script := "#!/bin/sh\ncat >/dev/null\necho '{\"protocol\": 1, \"name\": \"other\", \"kind\": \"" + kind + "\", \"version\": \"v0\"}'\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}

		_, err := (Plugin{Name: "other", Path: path}).Describe(context.Background(), t.TempDir())
		if !errors.Is(err, ErrKind) || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v", kind, err)
		}
	}
}
