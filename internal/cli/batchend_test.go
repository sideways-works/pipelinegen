package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestABatchEndsInAnotherStepWithOnlyItsOutputs(t *testing.T) {
	t.Parallel()

	for _, token := range []bool{false, true} {
		t.Run(map[bool]string{false: "no token", true: "token"}[token], func(t *testing.T) {
			t.Parallel()

			b := newPluginBatch(t, false, map[string]string{"go": "{}"})
			writeFile(t, filepath.Join(b.dir, "source", "pipelinegen.yaml"), readText(t, filepath.Join(b.root, "pipelinegen.yaml")))

			taskEnv := maps.Clone(b.env)
			if !token {
				taskEnv[envToken] = ""
			}

			slice := func(args ...string) {
				t.Helper()

				if _, stderr, code := runIn(t, b.dir, taskEnv, append(append([]string{"slice"}, args...), "--root", "source")...); code != 0 {
					t.Fatalf("slice %v: %d %s", args, code, stderr)
				}
			}

			slice("setup-end")
			slice("start", "e2e.a")
			b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")
			slice("end", "e2e.a")
			slice("start", "e2e.b")
			b.leave(t, "e2e.b", "go", "touched.txt", "go:notes#func:List")
			slice("end", "e2e.b")

			ensure := t.TempDir()
			for _, dir := range []string{"source", "batches", "tools", "result", "coverage"} {
				if err := os.CopyFS(filepath.Join(ensure, dir), os.DirFS(filepath.Join(b.dir, dir))); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.RemoveAll(b.dir); err != nil {
				t.Fatal(err)
			}

			if _, stderr, code := runIn(t, ensure, b.env, "report", "batch-end", "--batch", "1", "--result", "result", "--root", "source"); code != 0 {
				t.Fatalf("batch-end: %d %s", code, stderr)
			}

			for slice, id := range map[string]string{"e2e.a": "go:notes#func:Create", "e2e.b": "go:notes#func:List"} {
				if st := b.state(t, slice); st.State != report.Passed {
					t.Errorf("%s ended %s: %+v", slice, st.State, st.Failures)
				}

				if got := b.recorded(t, slice); !slices.Equal(got.Own, []string{id}) {
					t.Errorf("%s recorded %+v", slice, got)
				}
			}
		})
	}
}
