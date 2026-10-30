package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func TestBazelIsFoundTheWayAPipelineStepHasIt(t *testing.T) {
	build := t.TempDir()
	t.Chdir(build)
	t.Setenv("PATH", "/nonexistent")

	if err := os.MkdirAll("tools", 0o750); err != nil {
		t.Fatal(err)
	}

	if err := testenv.WriteExecutable(t.Context(), filepath.Join("tools", "bazelisk"), []byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(build, "tools", "bazelisk")
	cfg := &config.Config{Bazel: config.Bazel{Bazelisk: "bazelisk"}}

	t.Setenv("PIPELINEGEN_BAZELISK", "tools/bazelisk")

	if got := bazeliskFor(cfg, "trigger"); got != want {
		t.Errorf("from PIPELINEGEN_BAZELISK: %q", got)
	}

	t.Setenv("PIPELINEGEN_BAZELISK", "")

	if got := bazeliskFor(cfg, "trigger"); got != want {
		t.Errorf("fallback: %q", got)
	}

	c := common{root: "trigger"}
	if got := c.bazel(cfg).Bazelisk; got != want {
		t.Errorf("common client: %q", got)
	}

	if err := os.Remove(filepath.Join("tools", "bazelisk")); err != nil {
		t.Fatal(err)
	}

	_, err := c.bazel(cfg).Run(t.Context(), "version")
	if err == nil || !strings.Contains(err.Error(), "PIPELINEGEN_BAZELISK") || !strings.Contains(err.Error(), "tools/bazelisk") {
		t.Fatalf("no Bazel: %v", err)
	}
}
