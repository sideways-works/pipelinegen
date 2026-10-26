package driver

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func commandRequest(t *testing.T, conf map[string]any, batch bool, names ...string) plugin.DriverRequest {
	t.Helper()

	tree := t.TempDir()

	return plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(), Config: conf,
		Tree: tree, Batch: batch, Slices: slicesNamed(names...),
	}
}

func ends(t *testing.T, c *Command, req plugin.DriverRequest) map[string]plugin.DriverEvent {
	t.Helper()

	got := map[string]plugin.DriverEvent{}

	if _, err := c.Run(t.Context(), req, func(ev plugin.DriverEvent) { got[ev.Slice] = ev }); err != nil {
		t.Fatal(err)
	}

	return got
}

func TestEachJudgesByExitCodeOrByTheResultFile(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	script := `case "$1" in
  ok) exit 0 ;;
  bad) echo it broke; exit 1 ;;
  file) printf '{"passed":false,"tests":3,"failures":[{"test":"t2","tail":"no"}]}' > "$2"; exit 0 ;;
esac`
	req := commandRequest(t, map[string]any{"each": []any{"sh", "-c", script, "sh", "{name}", "{result}"}}, false, "ok", "bad", "file")

	got := ends(t, c, req)

	if !got["ok"].Passed || got["bad"].Passed || got["file"].Passed {
		t.Fatalf("ends: %+v", got)
	}

	if f := got["bad"].Failures; len(f) != 1 || !strings.Contains(f[0].Tail, "it broke") {
		t.Fatalf("bad: %+v", f)
	}

	if f := got["file"]; f.Tests == nil || *f.Tests != 3 || f.Failures[0].Test != "t2" {
		t.Fatalf("file: %+v", f)
	}
}

func TestAllWritesAResultPerSliceAndOneItSkippedGetsNoEnd(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	script := `for n in a b; do echo '{"passed":true}' > "$2/$n.json"; done`
	req := commandRequest(t, map[string]any{"all": []any{"sh", "-c", script, "sh", "{slices}", "{results}"}}, true, "a", "b", "lost")

	got := ends(t, c, req)

	if len(got) != 2 || !got["a"].Passed || !got["b"].Passed {
		t.Fatalf("ends: %+v", got)
	}
}

func TestABundleWithoutAllRunsEachInTurn(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	req := commandRequest(t, map[string]any{"each": []any{"true"}}, true, "a", "b")
	if got := ends(t, c, req); len(got) != 2 {
		t.Fatalf("ends: %+v", got)
	}
}

func TestASetupRunsThenStartsAndStops(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	req := commandRequest(t, map[string]any{
		"run":    []any{"sh", "-c", "echo installed > deps"},
		"start":  []any{"sh", "-c", "touch ready; exec sleep 600"},
		"ready":  map[string]any{"file": "ready", "seconds": 30},
		"export": map[string]any{"DEPS": "{source}/deps"},
	}, false)

	r, err := c.Setup(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	if r.Env()["DEPS"] != filepath.Join(req.Tree, "deps") {
		t.Fatalf("exports: %v", r.Env())
	}

	if data, err := os.ReadFile(filepath.Join(req.Tree, "deps")); err != nil || string(data) != "installed\n" {
		t.Fatalf("run before start: %q %v", data, err)
	}

	start := time.Now()

	if err := r.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if time.Since(start) > 15*time.Second {
		t.Fatalf("stop took %s", time.Since(start))
	}
}

func TestASetupThatExitsBeforeItIsReadyFails(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	req := commandRequest(t, map[string]any{
		"start": []any{"sh", "-c", "echo port taken; exit 3"},
		"ready": map[string]any{"file": "never", "seconds": 30},
	}, false)

	_, err := c.Setup(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "stopped before it was ready") || !strings.Contains(err.Error(), "port taken") {
		t.Fatalf("setup: %v", err)
	}
}

func TestASetupThatExitsZeroBeforeItIsReadySaysSo(t *testing.T) {
	c := &Command{Env: os.Environ(), Log: io.Discard}

	req := commandRequest(t, map[string]any{
		"start": []any{"sh", "-c", "echo daemonized"},
		"ready": map[string]any{"file": "never", "seconds": 30},
	}, false)

	_, err := c.Setup(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "stopped before it was ready (it exited 0)") || strings.Contains(err.Error(), "%!") {
		t.Fatalf("setup: %v", err)
	}
}

func TestMarkStandsForTheMarksCommand(t *testing.T) {
	got := fill([]string{"run", "{mark}", "start", "{name}"}, map[string]string{"name": "a"}, []string{"/t/pipelinegen", "slice", "mark"})
	if strings.Join(got, " ") != "run /t/pipelinegen slice mark start a" {
		t.Fatalf("filled: %v", got)
	}
}

// markScript is a marks command for a test: it makes the mark through the
// marks directory and waits for the batch's answer, as `pipelinegen slice
// mark` does, noting each mark in log.
func markScript(log string) []string {
	script := `d=$PIPELINEGEN_MARKS; id=$(date +%s%N)-$$; echo "mark $1 $2" >> "` + log + `"
printf '{"mark":"%s","slice":"%s"}' "$1" "$2" > "$d/.$id.tmp" && mv "$d/.$id.tmp" "$d/$id.json"
i=0; while [ ! -e "$d/$id.done" ]; do i=$((i+1)); [ $i -gt 500 ] && exit 1; sleep 0.02; done`

	return []string{"sh", "-c", script, "mark"}
}

func TestEachIsMarkedAroundInAMarkedBundle(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	c := &Command{Env: os.Environ(), Log: io.Discard}
	j := &journal{}
	r := &Runner{Hooks: j, Log: io.Discard, Record: true, Collects: true, Marks: plugin.Marks{Command: markScript(log), Dir: t.TempDir()}}

	req := commandRequest(t, map[string]any{"each": []any{"sh", "-c", `echo "work $0" >> "` + log + `"`, "{name}"}}, true)

	if err := r.Run(t.Context(), Step{Driver: c, Desc: plugin.Description{Apart: true}, Batch: true, Base: req}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	if want := []string{"start a", "stop a", "passed a (apart)", "start b", "stop b", "passed b (apart)"}; !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}

	want := "mark start a\nwork a\nmark stop a\nmark start b\nwork b\nmark stop b\n"
	if got := readLog(t, log); got != want {
		t.Fatalf("log:\n%s\nwant:\n%s", got, want)
	}
}

func readLog(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}
