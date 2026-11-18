package piperun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func writeChunk(t *testing.T, dir string, c int, keys ...string) {
	t.Helper()

	ch := Chunk{Chunk: c}
	for _, k := range keys {
		ch.Items = append(ch.Items, Item{Key: k})
	}

	data, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}

	tmp := filepath.Join(dir, ".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(tmp, filepath.Join(dir, itoa(c)+".json")); err != nil {
		t.Fatal(err)
	}
}

func itoa(c int) string { return strconv.Itoa(c) }

func TestNextReadsChunksInOrderUntilTheEnd(t *testing.T) {
	dir := t.TempDir()
	f := Feed{Dir: dir, Every: 5 * time.Millisecond}

	writeChunk(t, dir, 1, "a", "b")

	ch, ok, err := f.Next(t.Context(), 1)
	if err != nil || !ok || len(ch.Items) != 2 || ch.Items[1].Key != "b" {
		t.Fatalf("chunk 1: %+v %v %v", ch, ok, err)
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		writeChunk(t, dir, 2, "c")
	}()

	if ch, ok, err := f.Next(t.Context(), 2); err != nil || !ok || ch.Items[0].Key != "c" {
		t.Fatalf("chunk 2, written while it waited: %+v %v %v", ch, ok, err)
	}

	writeFile(t, filepath.Join(dir, EndFile), "")

	if _, ok, err := f.Next(t.Context(), 3); err != nil || ok {
		t.Fatalf("after the end: %v %v", ok, err)
	}
}

func TestAChunkWrittenBeforeTheEndIsStillRead(t *testing.T) {
	dir := t.TempDir()

	writeChunk(t, dir, 1, "a")
	writeFile(t, filepath.Join(dir, EndFile), "")

	if ch, ok, err := (Feed{Dir: dir}).Next(t.Context(), 1); err != nil || !ok || ch.Items[0].Key != "a" {
		t.Fatalf("%+v %v %v", ch, ok, err)
	}
}

func TestAChunkThatSaysAnotherNumberIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "1.json"), `{"chunk": 2, "items": []}`)

	if _, _, err := (Feed{Dir: dir}).Next(t.Context(), 1); !errors.Is(err, errChunk) {
		t.Fatalf("%v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	if _, _, err := (Feed{Dir: t.TempDir(), Every: time.Millisecond}).Next(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting past its context: %v", err)
	}
}

func TestEveryResultLineIsOneLineUnder4KB(t *testing.T) {
	dir := t.TempDir()
	r := &Results{Path: filepath.Join(dir, ResultsFile)}

	long := strings.Repeat("<ü&>", 3000)
	files := make([]string, 200)

	for i := range files {
		files[i] = "e2e/a/" + strings.Repeat("f", 40) + itoa(i%10) + ".png"
	}

	if err := r.Start("e2e/a"); err != nil {
		t.Fatal(err)
	}

	if err := r.Requeue("e2e/b", long); err != nil {
		t.Fatal(err)
	}

	if err := r.End(End{Key: "e2e/a", Outcome: Failed, Summary: long, Files: files[:3], DurationMS: 12, CPUSeconds: 1.5, PeakMB: 300}); err != nil {
		t.Fatal(err)
	}

	if err := r.End(End{Key: "e2e/c", Outcome: Failed, Summary: "short", Files: files}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}

	for _, l := range lines {
		if len(l)+1 > MaxLine || !utf8.ValidString(l) {
			t.Fatalf("a line of %d bytes: %.80s", len(l)+1, l)
		}
	}

	var a End
	if err := json.Unmarshal([]byte(lines[2]), &a); err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(a.Summary, cutMark) || len(a.Files) != 3 || a.PeakMB != 300 || strings.ContainsRune(a.Summary, utf8.RuneError) {
		t.Fatalf("the cut end: %d files, summary ending %q", len(a.Files), a.Summary[len(a.Summary)-20:])
	}

	var c End
	if err := json.Unmarshal([]byte(lines[3]), &c); err != nil {
		t.Fatal(err)
	}

	if c.Summary != "short" || len(c.Files) == 0 || len(c.Files) == len(files) || c.Files[0] != files[0] {
		t.Fatalf("files left out the last first: %d of %d", len(c.Files), len(files))
	}
}

func TestReportedTellsWhatEachItemAlreadyHas(t *testing.T) {
	p := filepath.Join(t.TempDir(), ResultsFile)
	writeFile(t, p, `{"key":"a","start":true}
{"key":"a","outcome":"passed"}
{"key":"b","start":true}
{"key":"c","requeue":true,"reason":"r"}
{"key":"d","requeue":true}
{"key":"d","start":true}
{"key":"a","start":true}
{"key":"e","outcome":"pass`)

	got, err := Reported(p)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"a": Ended, "b": Started, "c": Requeued, "d": Started}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}

func TestReadKeysLeavesOutALineStillBeingWritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "confirmed")
	writeFile(t, p, "e2e/a\n\n{\"key\": \"e2e/b\"}\ne2e/c")

	growing, err := ReadKeys(p, false)
	if err != nil || len(growing) != 2 || !growing["e2e/a"] || !growing["e2e/b"] {
		t.Fatalf("growing: %v %v", growing, err)
	}

	done, err := ReadKeys(p, true)
	if err != nil || len(done) != 3 || !done["e2e/c"] {
		t.Fatalf("complete: %v %v", done, err)
	}

	if none, err := ReadKeys(filepath.Join(t.TempDir(), "missing"), true); err != nil || len(none) != 0 {
		t.Fatalf("missing: %v %v", none, err)
	}

	writeFile(t, p, "{\"nokey\": 1}\n")

	if _, err := ReadKeys(p, true); !errors.Is(err, errList) {
		t.Fatalf("a line it can't read: %v", err)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFailureFilesKeepWithinTheItemsAndTheSessionsCaps(t *testing.T) {
	result, feed := t.TempDir(), t.TempDir()
	prefix := ".pipelinegen/files/e2e/a/"

	writeFile(t, filepath.Join(result, prefix, "shot.png"), strings.Repeat("x", 1<<20))
	writeFile(t, filepath.Join(result, prefix, "trace.zip"), strings.Repeat("x", 3<<19))
	writeFile(t, filepath.Join(result, prefix, "ctx.md"), "ctx")
	writeFile(t, filepath.Join(result, "secret"), "s")

	if err := os.Symlink(filepath.Join(result, "secret"), filepath.Join(result, prefix, "link")); err != nil {
		t.Fatal(err)
	}

	f := &Files{Feed: feed}

	kept, left, err := f.Kept(result, prefix, "e2e/a", []string{prefix + "shot.png", prefix + "trace.zip", prefix + "ctx.md", prefix + "link", prefix + "../../../../secret"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(kept, " ") != "e2e/a/shot.png e2e/a/ctx.md" || len(left) != 3 {
		t.Fatalf("kept %q, left %q", kept, left)
	}

	if data, err := os.ReadFile(filepath.Join(feed, FilesDir, "e2e/a/ctx.md")); err != nil || string(data) != "ctx" {
		t.Fatalf("%q %v", data, err)
	}

	f.used = MaxSessionFiles - 10

	kept, left, err = f.Kept(result, prefix, "e2e/b", []string{prefix + "shot.png", prefix + "ctx.md"})
	if err != nil || strings.Join(kept, " ") != "e2e/b/ctx.md" || len(left) != 1 {
		t.Fatalf("near the session's cap: kept %q, left %q, %v", kept, left, err)
	}
}
