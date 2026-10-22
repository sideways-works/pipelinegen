package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAJournalMovesWithItsBuild(t *testing.T) {
	task := t.TempDir()
	in := func(build string, p ...string) string { return filepath.Join(append([]string{build}, p...)...) }

	j := JournalIn(in(task, "result"))
	ev := SliceEvent{
		Suite: "e2e", Slice: "e2e.a", Result: in(task, "result", "e2e.a.json"), Extras: in(task, "result", "e2e.a"),
		Source: in(task, "source"), CoverageRoot: in(task, "coverage"),
		Coverage: []string{in(task, "coverage", "e2e.a"), in(task, "coverage", "_setup")},
		Parts:    []string{in(task, "coverage", "e2e.a")},
	}

	if _, err := j.load(); err != nil {
		t.Fatal(err)
	}

	if err := j.append(journalLine{Event: eventStart, At: time.Unix(1, 0), Slice: &ev}); err != nil {
		t.Fatal(err)
	}

	j.saveSummary()

	for _, f := range []string{j.Path, j.summaryPath()} {
		if data, err := os.ReadFile(f); err != nil || strings.Contains(string(data), task) {
			t.Fatalf("%s names the batch's directory: %v %s", f, err, data)
		}
	}

	ensure := t.TempDir()
	if err := os.CopyFS(in(ensure, "result"), os.DirFS(in(task, "result"))); err != nil {
		t.Fatal(err)
	}

	moved := JournalIn(in(ensure, "result"))

	lines, err := moved.read()
	if err != nil || len(lines) != 1 || lines[0].Slice == nil {
		t.Fatalf("lines %+v %v", lines, err)
	}

	got := lines[0].Slice
	if got.Result != in(ensure, "result", "e2e.a.json") || got.Extras != in(ensure, "result", "e2e.a") ||
		got.Source != in(ensure, "source") || got.CoverageRoot != in(ensure, "coverage") ||
		got.Coverage[1] != in(ensure, "coverage", "_setup") || got.Parts[0] != in(ensure, "coverage", "e2e.a") {
		t.Fatalf("read in the ensure: %+v", got)
	}

	sum, err := moved.load()
	if err != nil {
		t.Fatal(err)
	}

	if sum.Active == nil || sum.Active.Result != in(ensure, "result", "e2e.a.json") || sum.of(ev).Parts[0] != in(ensure, "coverage", "e2e.a") {
		t.Fatalf("summary in the ensure: %+v %+v", sum.Active, sum.of(ev))
	}
}
