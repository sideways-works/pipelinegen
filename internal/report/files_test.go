package report

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

func TestAFailureFilePathStaysInTheResultDirectory(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "../up", "a/../../up", "a/./b", "a//b", "a/", ".git/config", "a/.GIT/x", `a\b`, "a\nb", "a\x00b"} {
		if checkFilePath(p) == nil {
			t.Errorf("%q passed", p)
		}
	}

	for _, p := range []string{"a.png", "e2e/x/test-failed-1.png", ".pipelinegen/files/e2e/a.spec.ts/t/trace.zip", "a..b"} {
		if err := checkFilePath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestOnlyFilesInsideTheResultAreRead(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFileT(t, outside, "the token")
	writeFileT(t, filepath.Join(root, "shots", "a.png"), "png")
	writeFileT(t, filepath.Join(root, "big.bin"), strings.Repeat("x", 11))

	for link, to := range map[string]string{"out.txt": outside, "shots/up.txt": "../../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt", "in.png": "shots/a.png"} {
		if err := os.Symlink(to, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Mkdir(filepath.Join(root, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	list := []FailureFile{
		{Path: "shots/a.png", Label: "screenshot"},
		{Path: "in.png", Label: "a link inside"},
		{Path: "out.txt"}, {Path: "shots/up.txt"}, {Path: "../" + filepath.Base(outside)}, {Path: outside},
		{Path: "dir"}, {Path: "pipe"}, {Path: "missing.png"}, {Path: "big.bin"},
		{Path: "shots/a.png", Label: "named twice"},
	}

	done := make(chan struct{})

	var (
		got     []candidate
		dropped []DroppedFile
	)

	go func() {
		defer close(done)

		got, dropped = readFailureFiles(root, "e2e/a.spec.ts", list, 10)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading the files blocked, on the FIFO likely")
	}

	var kept []string
	for _, c := range got {
		kept = append(kept, c.kept.Path+"="+string(c.data))
	}

	if !slices.Equal(kept, []string{"shots/a.png=png", "in.png=png"}) {
		t.Errorf("kept %v", kept)
	}

	why := map[string]string{}
	for _, d := range dropped {
		why[d.Path] = d.Why
	}

	for p, want := range map[string]string{
		"out.txt": "not a plain path inside", "shots/up.txt": "not a plain path inside", "../secret.txt": "not a plain path inside",
		outside: "not a plain path inside", "dir": "not a regular file", "pipe": "not a regular file",
		"missing.png": "no such file", "big.bin": "over keep.files.slice: 10 bytes",
	} {
		if !strings.Contains(why[p], want) {
			t.Errorf("%s: %q, want %q", p, why[p], want)
		}
	}

	for _, d := range dropped {
		if strings.Contains(d.Why, root) {
			t.Errorf("%s: the reason names the machine's path: %s", d.Path, d.Why)
		}
	}
}

func TestTheLargestFilesGoFirst(t *testing.T) {
	got := []candidate{
		{kept: KeptFile{Path: "a", Bytes: 4}}, {kept: KeptFile{Path: "b", Bytes: 9}},
		{kept: KeptFile{Path: "c", Bytes: 3}}, {kept: KeptFile{Path: "d", Bytes: 5}},
	}

	kept, dropped := capFiles(got, 10, errOverSlice)

	var names []string
	for _, c := range kept {
		names = append(names, c.kept.Path)
	}

	if !slices.Equal(names, []string{"a", "c"}) || len(dropped) != 2 || dropped[0].Path != "b" || dropped[1].Path != "d" ||
		dropped[0].Why != "over keep.files.slice: 10 bytes" {
		t.Fatalf("kept %v, dropped %+v", names, dropped)
	}

	if kept, dropped := capFiles(got, 0, errOverRun); len(kept) != 0 || len(dropped) != 4 {
		t.Fatalf("a cap of 0: kept %d, dropped %d", len(kept), len(dropped))
	}
}

// failingWithFiles writes a result directory as a batch leaves it: the
// slice's result naming files in its files directory, and those files.
func failingWithFiles(t *testing.T, slice, result string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	for p, body := range files {
		writeFileT(t, filepath.Join(FilesDir(dir, slice), filepath.FromSlash(p)), body)
	}

	p := filepath.Join(dir, filepath.FromSlash(slice)+".json")
	writeFileT(t, p, result)

	return p
}

func filesResult(passed bool, paths ...string) string {
	var b strings.Builder

	if passed {
		b.WriteString(`{"passed": true, "files": [`)
	} else {
		b.WriteString(`{"passed": false, "failures": [{"test": "t", "tail": "boom"}], "files": [`)
	}

	for i, p := range paths {
		if i > 0 {
			b.WriteString(", ")
		}

		b.WriteString(`{"path": ".pipelinegen/files/` + p + `", "label": "` + filepath.Base(p) + `"}`)
	}

	b.WriteString("]}")

	return b.String()
}

func TestFailureFilesAreKeptForFailedSlicesOnly(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	secret.Add("ghs_filesecret")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	// a screenshot is binary: masking would break it, so it goes as it is
	png := "\x89PNG\r\n\x1a\n\x00\x00 ghs_filesecret \xff\xfe"
	failed := failingWithFiles(t, "e2e.checkout", filesResult(false, "e2e.checkout/t1/test-failed-1.png", "e2e.checkout/t1/error-context.md"),
		map[string]string{"t1/test-failed-1.png": png, "t1/error-context.md": "# Page\nGITHUB_TOKEN=ghs_filesecret\n"})
	passed := failingWithFiles(t, "e2e.orders", filesResult(true, "e2e.orders/a.png"), map[string]string{"a.png": "png"})

	if _, err := r.Slices(ctx, []SliceEvent{
		{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Result: failed, Coverage: []string{"c"}},
		{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Result: passed, Coverage: []string{"c"}},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := RunFiles(ctx, r.Store, r.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(list) != 1 || list[0].Slice != "e2e.checkout" || list[0].State != Failed || len(list[0].Files) != 2 {
		t.Fatalf("slices with files: %+v", list)
	}

	if f := list[0].Files[0]; f.Path != "t1/test-failed-1.png" || f.Label != "test-failed-1.png" || f.Bytes != int64(len(png)) {
		t.Fatalf("kept %+v", f)
	}

	out := t.TempDir()

	if n, err := WriteFiles(ctx, r.Store, list, out); err != nil || n != 2 {
		t.Fatalf("wrote %d: %v", n, err)
	}

	got, err := os.ReadFile(filepath.Join(out, "e2e", "e2e.checkout", "t1", "test-failed-1.png"))
	if err != nil || !bytes.Equal(got, []byte(png)) {
		t.Fatalf("the screenshot came back as %q: %v", got, err)
	}

	got, err = os.ReadFile(filepath.Join(out, "e2e", "e2e.checkout", "t1", "error-context.md"))
	if err != nil || string(got) != "# Page\nGITHUB_TOKEN=***\n" {
		t.Fatalf("the error context came back as %q: %v", got, err)
	}

	if _, err := os.Stat(filepath.Join(out, "e2e", "e2e.orders")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a passed slice's files were written: %v", err)
	}

	// a whole-suite check never gets them
	slicesDir := t.TempDir()
	if _, err := Collect(ctx, r.Store, r.ID, "e2e", slicesDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(slicesDir, "e2e.checkout", "files")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collect wrote the failure files: %v", err)
	}

	body := onlyBody(t, gh, 7)
	if !strings.Contains(body, "(2 failure files)") || !strings.Contains(body, "`pipelinegen report files --run pr --change pr-7 --commit 0123456789ab --out failure-files`") ||
		strings.Contains(body, "PNG") || strings.Contains(body, "# Page") {
		t.Fatalf("comment:\n%s", body)
	}

	if list, err := RunFiles(ctx, r.Store, r.ID, "e2e", "//e2e:checkout"); err != nil || len(list) != 1 {
		t.Fatalf("by label: %+v %v", list, err)
	}
}

func TestFailureFilesKeepWithinTheirCaps(t *testing.T) {
	ctx := t.Context()
	r := newReporter(t, bareRemote(t), newFake(), "")
	r.Config.Keep.Files = config.KeepFiles{Slice: "10KB", Run: "12KB"}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	kb := func(n int) string { return strings.Repeat("x", n<<10) }

	// over the slice's cap the largest goes; a file over it alone is
	// never read
	first := failingWithFiles(t, "e2e/a.spec.ts", filesResult(false, "e2e/a.spec.ts/big.zip", "e2e/a.spec.ts/mid.png", "e2e/a.spec.ts/small.md", "e2e/a.spec.ts/huge.zip"),
		map[string]string{"big.zip": kb(6), "mid.png": kb(4), "small.md": kb(2), "huge.zip": kb(11)})
	if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/a.spec.ts", Label: "a", Result: first}); err != nil {
		t.Fatal(err)
	}

	// the run holds 6 KB of its 12: of the next slice's 6 + 3 KB, the 6 go
	second := failingWithFiles(t, "e2e/b.spec.ts", filesResult(false, "e2e/b.spec.ts/trace.zip", "e2e/b.spec.ts/shot.png"),
		map[string]string{"trace.zip": kb(6), "shot.png": kb(3)})
	if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/b.spec.ts", Label: "b", Result: second}); err != nil {
		t.Fatal(err)
	}

	// written again, a slice's own files count once
	if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/b.spec.ts", Label: "b", Result: second}); err != nil {
		t.Fatal(err)
	}

	list, err := RunFiles(ctx, r.Store, r.ID, "lint", "")
	if err != nil {
		t.Fatal(err)
	}

	got := map[string][]string{}

	for _, sf := range list {
		for _, f := range sf.Files {
			got[sf.Slice] = append(got[sf.Slice], f.Path)
		}

		for _, d := range sf.Dropped {
			got[sf.Slice] = append(got[sf.Slice], "-"+d.Path+": "+d.Why)
		}
	}

	want := map[string][]string{
		"e2e/a.spec.ts": {"mid.png", "small.md", "-huge.zip: over keep.files.slice: 10 KB", "-big.zip: over keep.files.slice: 10 KB"},
		"e2e/b.spec.ts": {"shot.png", "-trace.zip: over keep.files.run: 12 KB"},
	}

	for slice, w := range want {
		if !slices.Equal(got[slice], w) {
			t.Errorf("%s: %q, want %q", slice, got[slice], w)
		}
	}

	r.Config.Keep.Files.Slice = "0"

	none := failingWithFiles(t, "e2e/c.spec.ts", filesResult(false, "e2e/c.spec.ts/a.png"), map[string]string{"a.png": "x"})
	if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/c.spec.ts", Label: "c", Result: none}); err != nil {
		t.Fatal(err)
	}

	if list, err := RunFiles(ctx, r.Store, r.ID, "lint", "e2e/c.spec.ts"); err != nil || len(list) != 0 {
		t.Fatalf("keep.files.slice 0 kept %+v %v", list, err)
	}
}

func TestAStoreThatRefusesTheFilesStillGetsTheStates(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)

	if out, err := exec.CommandContext(ctx, "git", "-C", strings.TrimPrefix(remote, "file://"), "config", "receive.maxInputSize", "200000").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	r := newReporter(t, remote, newFake(), "")
	r.Store.Retries = 0

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	// random bytes, which no pack shrinks below the remote's limit
	noise := make([]byte, 600<<10)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}

	result := failingWithFiles(t, "e2e/a.spec.ts", filesResult(false, "e2e/a.spec.ts/trace.zip"), map[string]string{"trace.zip": string(noise)})

	state, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/a.spec.ts", Label: "a", Result: result})
	if err != nil || state != Failed {
		t.Fatalf("%s %v", state, err)
	}

	list, err := RunFiles(ctx, r.Store, r.ID, "lint", "")
	if err != nil || len(list) != 1 || len(list[0].Files) != 0 || len(list[0].Dropped) != 1 ||
		!strings.HasPrefix(list[0].Dropped[0].Why, "the store refused the write with them") {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestTheSweepTakesTheFilesWithTheRun(t *testing.T) {
	ctx := t.Context()
	r := newReporter(t, bareRemote(t), newFake(), "")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	result := failingWithFiles(t, "e2e/a.spec.ts", filesResult(false, "e2e/a.spec.ts/a.png"), map[string]string{"a.png": "png"})
	if _, err := r.Slice(ctx, SliceEvent{Suite: "lint", Slice: "e2e/a.spec.ts", Label: "a", Result: result}); err != nil {
		t.Fatal(err)
	}

	if list, err := RunFiles(ctx, r.Store, r.ID, "", ""); err != nil || len(list) != 1 {
		t.Fatalf("before the sweep: %+v %v", list, err)
	}

	if _, err := Sweep(ctx, r.Store, time.Now().Add(30*24*time.Hour), Keep{Runs: KeepRuns{Age: 7 * 24 * time.Hour}, Lists: KeepLists{Age: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := RunFiles(ctx, testStore(t, r.Store.Remote), r.ID, "", ""); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("after the sweep: %v", err)
	}
}

func TestADriversFilesAreKeptByTheirOwnPaths(t *testing.T) {
	if got := storedName("e2e/a.spec.ts", ".pipelinegen/files/e2e/a.spec.ts/t1/trace.zip"); got != "t1/trace.zip" {
		t.Errorf("a driver's: %s", got)
	}

	if got := storedName("e2e/a.spec.ts", "shots/a.png"); got != "shots/a.png" {
		t.Errorf("a task's: %s", got)
	}

	if got := FilesCommand("pr", "pr-7", commit, "e2e/it's.spec.ts"); got != `pipelinegen report files --run pr --change pr-7 --commit 0123456789ab --slice 'e2e/it'\''s.spec.ts' --out failure-files` {
		t.Errorf("command: %s", got)
	}
}

func TestAWriteWithFilesRetriesForHalfTheTime(t *testing.T) {
	store := &gitstore.Store{RetryFor: 10 * time.Minute}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	with := []*ending{{found: []candidate{{}}}}

	restore := filesUntil(store, with, now)
	if want := now.Add(5 * time.Minute); !store.Until.Equal(want) {
		t.Fatalf("until %v, want %v", store.Until, want)
	}

	restore()

	if !store.Until.IsZero() {
		t.Fatalf("not put back: %v", store.Until)
	}

	store.Until = now.Add(time.Minute)
	filesUntil(store, with, now)()

	if !store.Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("a nearer bound moved: %v", store.Until)
	}

	filesUntil(store, nil, now)

	if !store.Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("a write without files moved it: %v", store.Until)
	}
}
