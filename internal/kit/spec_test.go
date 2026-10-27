package kit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/spec"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// PIPELINEGEN_WRITE_SPEC=1 writes the embedded spec anew.
func TestSpecIsTheFixtures(t *testing.T) {
	cfg, err := config.Load(filepath.Join(testrepo.FixtureDir(t), "..", "kit", "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	want, err := spec.From(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if os.Getenv("PIPELINEGEN_WRITE_SPEC") == "1" {
		data, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile("spec.json", append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := Spec(); !reflect.DeepEqual(canonical(got), canonical(want)) {
		t.Fatal("spec.json is out of date: run the test with PIPELINEGEN_WRITE_SPEC=1")
	}
}
