package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestADriversFilesAreNamedFromTheResultDirectory(t *testing.T) {
	result := t.TempDir()
	p := filepath.Join(result, "e2e", "a.spec.ts.json")

	ev := &plugin.DriverEvent{Slice: "e2e/a.spec.ts", Event: plugin.EventEnd, Failures: []plugin.Failure{{Test: "t"}},
		Files: []plugin.File{{Path: "t1/trace.zip", Label: "trace"}, {Path: "../../../../etc/passwd"}}}
	if err := writeResult(p, ev, ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	var r report.ResultFile
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}

	// the escape stays one: the reporter refuses any path with ".." in it
	if len(r.Files) != 2 || r.Files[0].Path != ".pipelinegen/files/e2e/a.spec.ts/t1/trace.zip" || r.Files[1].Path != ".pipelinegen/files/e2e/a.spec.ts/../../../../etc/passwd" {
		t.Fatalf("files: %+v", r.Files)
	}

	if got := report.FilesDir(result, "e2e/a.spec.ts"); got != filepath.Join(result, ".pipelinegen", "files", "e2e", "a.spec.ts") {
		t.Fatalf("files directory: %s", got)
	}
}
