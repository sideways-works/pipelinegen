package cli

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestAnInterpreterIsReadFromTheFirstLine(t *testing.T) {
	dir := t.TempDir()

	for name, body := range map[string]string{
		"sh":      "#!/bin/sh\necho\n",
		"env":     "#!/usr/bin/env sh\necho\n",
		"envS":    "#!/usr/bin/env -S sh -e\necho\n",
		"nonode":  "#!/usr/bin/env no-such-interpreter-here\n",
		"nopath":  "#!/no/such/interpreter\n",
		"binary":  "\x7fELF\x02\x01\x01",
		"noshell": "console.log(1)\n",
	} {
		writeFile(t, filepath.Join(dir, name), body)
	}

	for name, want := range map[string]struct {
		prog    string
		missing bool
	}{
		"sh": {"/bin/sh", false}, "env": {"sh", false}, "envS": {"sh", false},
		"nonode": {"no-such-interpreter-here", true}, "nopath": {"/no/such/interpreter", true},
		"binary": {"", false}, "noshell": {"", false}, "absent": {"", false},
	} {
		if prog, missing := interpreter(filepath.Join(dir, name)); prog != want.prog || missing != want.missing {
			t.Errorf("%s: %q %t, want %q %t", name, prog, missing, want.prog, want.missing)
		}
	}
}

func TestABatchWithoutACorrelatorsInterpreterStopsBeforeItsFirstSlice(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	p := filepath.Join(f.root, "tools", "plugins", "fake")
	writeFile(t, p, strings.Replace(readFile(t, p), "#!/bin/sh\n", "#!/usr/bin/env no-such-interpreter-here\n", 1))

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code == 0 || !strings.Contains(stderr, "suite e2e records with fake, which needs no-such-interpreter-here; this task (slice run) has none") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	if _, err := os.Stat(f.driverLog); err == nil {
		t.Fatalf("a slice ran: %s", readFile(t, f.driverLog))
	}
}

// The correlator file's interpreter is on PATH only while bin is, as in the batch's task and not its end's.
type observedBatch struct {
	b       *batchPlugins
	j       *report.Journal
	evs     []report.SliceEvent
	bin     string
	cleanup func()
}

func newObservedBatch(t *testing.T, correlatorConfig string) *observedBatch {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	tools, result, coverage := filepath.Join(dir, "tools"), filepath.Join(dir, "result"), filepath.Join(dir, "coverage")

	bin := filepath.Join(dir, "bin")
	writeFile(t, filepath.Join(bin, "fakeinterp"), "#!/bin/sh\nexec sh \"$@\"\n")

	writeFile(t, filepath.Join(tools, "plugins", "fake"), "#!/usr/bin/env fakeinterp\nFAKE=1 exec '"+exe+"' \"$@\"\n")

	for _, p := range []string{filepath.Join(bin, "fakeinterp"), filepath.Join(tools, "plugins", "fake")} {
		if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // the test's own programs
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg, err := config.Parse([]byte(`pipeline: shop
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    bind: recorded
    run_slice: ci/e2e.yml
plugins:
  - {name: fake, kind: correlator, repository: example/fake, commit: 0123456789abcdef0123456789abcdef01234567, config: ` + correlatorConfig + `}
`))
	if err != nil {
		t.Fatal(err)
	}

	b, err := loadCorrelators(t.Context(), cfg, "e2e", tools, filepath.Join(result, report.JournalDir, describedFile))
	if err != nil {
		t.Fatal(err)
	}

	b.observed = filepath.Join(result, report.JournalDir, observedDir)

	j := report.JournalIn(result)
	r := &report.Reporter{Config: cfg, Warn: io.Discard, NoToken: true, NoFlush: true}

	var evs []report.SliceEvent

	for _, name := range []string{"e2e/a.spec.ts", "e2e/b.spec.ts"} {
		ev := sliceEvent("e2e", batchSlice{Name: name}, dir, result, coverage)
		writeFile(t, filepath.Join(ev.Coverage[0], "fake", "touched.txt"), "unit:"+name+"\n")
		writeFile(t, ev.Result, `{"passed": true}`)

		if err := r.Started(t.Context(), j, ev, false, time.Hour); err != nil {
			t.Fatal(err)
		}

		if _, err := r.Ended(t.Context(), j, ev, time.Hour); err != nil {
			t.Fatal(err)
		}

		evs = append(evs, ev)
	}

	return &observedBatch{b: b, j: j, evs: evs, bin: bin, cleanup: func() { _ = b.close() }} //nolint:errcheck // the test's own scratch
}

func (o *observedBatch) end(t *testing.T) map[string]string {
	t.Helper()

	t.Setenv("PATH", strings.TrimPrefix(os.Getenv("PATH"), o.bin+string(os.PathListSeparator)))

	out := map[string]string{}

	for _, ev := range o.evs {
		setup, parts := report.ObserveInputs(ev)

		obs, err := o.b.Observe(t.Context(), ev.Source, ev.Slice, setup, parts)
		if err != nil {
			out[ev.Slice] = "error: " + err.Error()

			continue
		}

		var got []string
		for _, ob := range obs {
			got = append(got, ob.Touched...)
		}

		out[ev.Slice] = strings.Join(got, " ")
	}

	return out
}

func TestABatchObservesWhereItRan(t *testing.T) {
	o := newObservedBatch(t, "{}")
	defer o.cleanup()

	var log strings.Builder

	o.b.observeAhead(t.Context(), o.j, "e2e", time.Now().Add(time.Hour), &log)

	if log.Len() != 0 {
		t.Fatalf("the batch left observe to its end: %s", log.String())
	}

	got := o.end(t)
	for _, name := range []string{"e2e/a.spec.ts", "e2e/b.spec.ts"} {
		if got[name] != "unit:"+name {
			t.Errorf("%s: %q", name, got[name])
		}
	}
}

func TestAnObserveTheBatchCouldNotMakeIsTheBatchsEnds(t *testing.T) {
	o := newObservedBatch(t, "{}")
	defer o.cleanup()

	var log strings.Builder

	o.b.observeAhead(t.Context(), o.j, "e2e", time.Now().Add(-time.Second), &log)

	if !strings.Contains(log.String(), "observe for fake stays with the batch's end: the task's time ran out") {
		t.Fatalf("log: %q", log.String())
	}

	t.Setenv(envSuite, "e2e")

	for slice, got := range o.end(t) {
		if !strings.Contains(got, "suite e2e records with fake, which needs fakeinterp; this task (report batch-end) has none") {
			t.Errorf("%s: %q", slice, got)
		}
	}

	var said strings.Builder

	o.b.sayLeftUnrunnable("e2e", o.evs, &said)

	if got := said.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "fakeinterp; this task (report batch-end) has none: 2 slices not observed in the batch fail") {
		t.Fatalf("said: %q", got)
	}
}

func TestAnObserveThatFailsInTheBatchIsTheBatchsEnds(t *testing.T) {
	o := newObservedBatch(t, "{fail: observe}")
	defer o.cleanup()

	var log strings.Builder

	o.b.observeAhead(t.Context(), o.j, "e2e", time.Now().Add(time.Hour), &log)

	if !strings.Contains(log.String(), "observe for fake stays with the batch's end: ") {
		t.Fatalf("log: %q", log.String())
	}

	if _, ok := o.b.kept("e2e/a.spec.ts", "fake"); ok {
		t.Fatal("a failed observe was kept")
	}
}

func TestARecordKeepsTheBatchsObservations(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)
	work := filepath.Join(t.TempDir(), "w")

	if stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root, "--work", work); code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	for _, name := range []string{"e2e/cart.spec.ts", "e2e/pay.spec.ts"} {
		b := &batchPlugins{observed: filepath.Join(work, "build", "result", report.JournalDir, observedDir)}
		if _, ok := b.kept(name, "fake"); !ok {
			t.Errorf("%s: no answer kept by the batch", name)
		}
	}

	if l := recordedLists(t, f.root); l["e2e/e2e/cart.spec.ts"] != "unit:e2e/cart.spec.ts" {
		t.Fatalf("lists: %v", l)
	}
}

func TestACorrelatorWhoseInterpreterOnlyTheBatchHasRecords(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "fakeinterp"), `#!/bin/sh
case "$(tr '\0' ' ' < /proc/$PPID/cmdline)" in
*"report batch-end"*) echo "fakeinterp: not in this image" >&2; exit 127 ;;
esac
exec sh "$@"
`)

	if err := os.Chmod(filepath.Join(bin, "fakeinterp"), 0o700); err != nil { //nolint:gosec // the test's own program
		t.Fatal(err)
	}

	p := filepath.Join(f.root, "tools", "plugins", "fake")
	writeFile(t, p, strings.Replace(readFile(t, p), "#!/bin/sh\n", "#!/usr/bin/env fakeinterp\n", 1))

	env := map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	maps.Copy(env, f.env)

	stdout, stderr, code := runIn(t, f.root, env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 || strings.Contains(stderr, "not in this image") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	if l := recordedLists(t, f.root); l["e2e/e2e/cart.spec.ts"] != "unit:e2e/cart.spec.ts" || l["e2e/e2e/pay.spec.ts"] != "unit:e2e/pay.spec.ts" {
		t.Fatalf("lists: %v", l)
	}
}
