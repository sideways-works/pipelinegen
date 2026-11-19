package cli

import (
	"encoding/json"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestBookkeepingFilesAreTheConfigTheSameEveryTime(t *testing.T) {
	tree := t.TempDir()
	conf := "pipeline: shop\nsuites: {}\n"

	for p, body := range map[string]string{config.FileName: conf, "src/app.go": "package app\n", "ci/e2e.yml": "platform: linux\n"} {
		writeFile(t, filepath.Join(tree, p), body)
	}

	gitT(t, tree, "init", "-q")

	var outs []string

	for range 2 {
		out := filepath.Join(t.TempDir(), "bookkeeping")
		if _, stderr, code := runIn(t, t.TempDir(), nil, "slice", "bookkeeping-files", "--root", tree, "--out", out); code != 0 {
			t.Fatalf("bookkeeping-files: %d %s", code, stderr)
		}

		outs = append(outs, out)
	}

	for _, out := range outs {
		var files []string

		if err := filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(out, p) //nolint:errcheck // p is under out
				files = append(files, rel)
			}

			return err
		}); err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(files, []string{config.FileName}) {
			t.Errorf("%s holds %v, want the config alone", out, files)
		}
	}

	first, second := readText(t, filepath.Join(outs[0], config.FileName)), readText(t, filepath.Join(outs[1], config.FileName))
	if first != conf || second != first {
		t.Errorf("the config written: %q, then %q, want %q", first, second, conf)
	}
}

// keep writes what a driven batch keeps of a slice's observation (batchPlugins.keep).
func keep(t *testing.T, result, slice, correlator string, touched ...string) {
	t.Helper()

	data, err := json.Marshal([]keptObservation{{Plugin: correlator, Touched: touched}})
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(result, report.JournalDir, observedDir, url.PathEscape(slice), correlator+".json"), string(data))
}

func TestABatchEndsFromItsBookkeepingAsFromItsTree(t *testing.T) {
	t.Parallel()

	for _, kept := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed in the batch", false: "left unobserved"}[kept], func(t *testing.T) {
			t.Parallel()

			b := newPluginBatch(t, false, map[string]string{"go": "{}"})
			writeFile(t, filepath.Join(b.dir, "source", config.FileName), readText(t, filepath.Join(b.root, config.FileName)))

			taskEnv := maps.Clone(b.env)
			taskEnv[envToken] = ""

			slice := func(args ...string) {
				t.Helper()

				if _, stderr, code := runIn(t, b.dir, taskEnv, append(append([]string{"slice"}, args...), "--root", "source")...); code != 0 {
					t.Fatalf("slice %v: %d %s", args, code, stderr)
				}
			}

			slice("setup-end")

			for s, id := range map[string]string{"e2e.a": "go:notes#func:Create", "e2e.b": "go:notes#func:List"} {
				slice("start", s)
				b.leave(t, s, "go", "touched.txt", id)
				slice("end", s)

				if kept {
					keep(t, filepath.Join(b.dir, "result"), s, "go", id)
				}
			}

			if _, stderr, code := runIn(t, b.dir, nil, "slice", "bookkeeping-files", "--root", "source", "--out", "bookkeeping"); code != 0 {
				t.Fatalf("bookkeeping-files: %d %s", code, stderr)
			}

			// the end runs where the batch's outputs and its bookkeeping are, and no tree
			end := t.TempDir()
			for _, dir := range []string{"bookkeeping", "batches", "tools", "result", "coverage"} {
				if err := os.CopyFS(filepath.Join(end, dir), os.DirFS(filepath.Join(b.dir, dir))); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.RemoveAll(b.dir); err != nil {
				t.Fatal(err)
			}

			_, stderr, code := runIn(t, end, b.env, "report", "batch-end", "--batch", "1", "--result", "result", "--root", "bookkeeping")
			if code != 0 {
				t.Fatalf("batch-end: %d %s", code, stderr)
			}

			for s, id := range map[string]string{"e2e.a": "go:notes#func:Create", "e2e.b": "go:notes#func:List"} {
				if st := b.state(t, s); st.State != report.Passed {
					t.Errorf("%s ended %s: %+v", s, st.State, st.Failures)
				}

				got := b.recorded(t, s)

				switch {
				case kept && !slices.Equal(got.Own, []string{id}):
					t.Errorf("%s recorded %+v, want what the batch observed", s, got)
				case !kept && (len(got.CannotTell) != 1 || !strings.Contains(got.CannotTell[0].Reason, noTreeToObserve)):
					t.Errorf("%s recorded %+v, want can't tell for want of the tree", s, got)
				}
			}

			if !kept && !strings.Contains(stderr, noTreeToObserve) {
				t.Errorf("the end didn't say why its slices can't be told: %s", stderr)
			}
		})
	}
}
