package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeDriver fails a slice whose name holds "bad"; die_after, deaf, env, commit and work in its config
// shape the run.
func fakeDriver() plugin.DriverHandlers {
	return plugin.DriverHandlers{
		Describe: func() (plugin.Description, error) {
			return plugin.Description{
				Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "stub", Version: "v0",
				Verbs: []string{plugin.CallRun, plugin.CallSetup}, Routes: &plugin.Routes{Files: []string{"**"}}, Apart: true,
			}, nil
		},
		Run: func(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error) error {
			leaked := os.Getenv(plugin.EnvTrusted) != "" || os.Getenv("GITHUB_TOKEN") != ""

			if err := note(req.Config, fmt.Sprintf("run batch=%t marks=%t leaked=%t %s", req.Batch, req.Marks != nil, leaked, strings.Join(driver.Names(req.Slices), ","))); err != nil {
				return err
			}

			if req.Config["env"] == true {
				if err := note(req.Config, "env marks="+os.Getenv("FAKE_MARKS")); err != nil {
					return err
				}
			}

			if req.Config["commit"] == true {
				if err := note(req.Config, "commit="+os.Getenv("FAKE_COMMIT")+" change="+os.Getenv("FAKE_CHANGE")); err != nil {
					return err
				}
			}

			dieAfter, _ := req.Config["die_after"].(float64) //nolint:errcheck // absent: never

			for i, sl := range req.Slices {
				if dieAfter > 0 && len(req.Slices) > 1 && i == int(dieAfter) {
					return errFake
				}

				if err := fakeSlice(ctx, req, sl, emit); err != nil {
					return err
				}
			}

			return nil
		},
		Setup: func(_ context.Context, req plugin.DriverRequest, ready func(map[string]string) error, stop <-chan struct{}) error {
			if err := ready(map[string]string{"STACK_URL": "http://127.0.0.1:1"}); err != nil {
				return err
			}

			<-stop

			return note(req.Config, "setup stopped")
		},
	}
}

func fakeSlice(ctx context.Context, req plugin.DriverRequest, sl plugin.DriverSlice, emit func(plugin.DriverEvent) error) error {
	mark := func(what string) error {
		if req.Marks == nil || req.Config["deaf"] == true {
			return nil
		}

		return driver.MarkAndWait(ctx, req.Marks.Dir, driver.Mark{Mark: what, Slice: sl.Name}, 30*time.Second)
	}

	if err := mark(plugin.MarkStart); err != nil {
		return err
	}

	if req.Config["work"] == true {
		if err := note(req.Config, "work "+sl.Name); err != nil {
			return err
		}

		time.Sleep(50 * time.Millisecond)
	}

	if req.Record {
		dir := filepath.Join(sl.Recording, "fake")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("recording: %w", err)
		}

		if err := os.WriteFile(filepath.Join(dir, "touched.txt"), []byte("unit:"+sl.Name+"\n"), 0o600); err != nil {
			return fmt.Errorf("recording: %w", err)
		}
	}

	if err := mark(plugin.MarkStop); err != nil {
		return err
	}

	one := 1

	ev := plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true, Tests: &one, Recorded: req.Record}
	if strings.Contains(sl.Name, "bad") {
		ev.Passed, ev.Failures = false, []plugin.Failure{{Test: "it", Tail: "broke"}}
	}

	return emit(ev)
}

const drivenConfig = `pipeline: shop
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    bind: recorded
    stack: STACK
    steps: STEPS
plugins:
  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}
  - {name: fake, kind: correlator, repository: example/fake, commit: 0123456789abcdef0123456789abcdef01234567, config: {log: CORRELATOR}}
  - {name: stub, kind: driver, repository: example/stub, commit: 0123456789abcdef0123456789abcdef01234567}
`

type drivenFixture struct {
	root, driverLog, correlatorLog string
	env                            map[string]string
}

func drivenRepo(t *testing.T, steps string, stack bool) drivenFixture {
	t.Helper()

	root := t.TempDir()
	logs := t.TempDir()
	driverLog, correlatorLog := filepath.Join(logs, "driver"), filepath.Join(logs, "correlator")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := strings.NewReplacer("STEPS", strings.ReplaceAll(steps, "LOG", driverLog), "STACK", strconv.FormatBool(stack), "CORRELATOR", correlatorLog).Replace(drivenConfig)

	for p, body := range map[string]string{
		config.FileName:      cfg,
		"e2e/cart.spec.ts":   "test('cart')\n",
		"e2e/pay.spec.ts":    "test('pay')\n",
		"tools/plugins/fake": "#!/bin/sh\nFAKE=1 exec '" + exe + "' \"$@\"\n",
		// The test binary drops every PIPELINEGEN_ variable it starts with, so the marks go under another name.
		"tools/plugins/stub": "#!/bin/sh\nFAKE_DRIVER=1 FAKE_MARKS=\"${" + plugin.EnvMarks + ":-}\" FAKE_COMMIT=\"${" + plugin.EnvCommit + ":-}\" FAKE_CHANGE=\"${PIPELINEGEN_CHANGE:-}\" exec '" + exe + "' \"$@\"\n",
	} {
		writeFile(t, filepath.Join(root, p), body)
	}

	for _, p := range []string{"tools/plugins/fake", "tools/plugins/stub"} {
		if err := os.Chmod(filepath.Join(root, p), 0o700); err != nil { //nolint:gosec // the test's own plugins
			t.Fatal(err)
		}
	}

	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "add", "-A", ":!tools")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "one")

	return drivenFixture{
		root: root, driverLog: driverLog, correlatorLog: correlatorLog,
		env: map[string]string{envGitURL: "", envToken: "a-token-the-driver-never-sees", envJobURL: "", envRun: "", envConfig: ""},
	}
}

func drivenLog(t *testing.T, p string) []string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func recordedLists(t *testing.T, root string) map[string]string {
	t.Helper()

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	var warn strings.Builder

	l, _ := baseLists(t.Context(), cfg, root, gitT(t, root, "rev-parse", "HEAD"), listsRemote, &warn)

	out := map[string]string{}

	for k, ids := range l {
		var s []string
		for _, id := range ids {
			s = append(s, string(id))
		}

		out[k] = strings.Join(s, " ")
	}

	return out
}

func TestRecordRunsASuiteThroughItsDriver(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 || !strings.Contains(stdout, "recorded 2 slices of e2e") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	if got := drivenLog(t, f.driverLog); !slices.Equal(got, []string{"run batch=true marks=true leaked=false e2e/cart.spec.ts,e2e/pay.spec.ts"}) {
		t.Fatalf("driver calls: %q", got)
	}

	l := recordedLists(t, f.root)
	for _, s := range []string{"e2e/cart.spec.ts", "e2e/pay.spec.ts"} {
		if l["e2e/"+s] != "unit:"+s {
			t.Errorf("%s's list: %q (all: %v)", s, l["e2e/"+s], l)
		}
	}
}

func TestWithoutBatchEverySliceIsACallOfItsOwn(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, with: {log: LOG}}]", false)

	if stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root); code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	want := []string{"run batch=false marks=false leaked=false e2e/cart.spec.ts", "run batch=false marks=false leaked=false e2e/pay.spec.ts"}
	if got := drivenLog(t, f.driverLog); !slices.Equal(got, want) {
		t.Fatalf("driver calls: %q", got)
	}

	if l := recordedLists(t, f.root); l["e2e/e2e/pay.spec.ts"] != "unit:e2e/pay.spec.ts" {
		t.Fatalf("lists: %v", l)
	}
}

func TestSlicesADeadBundleLeftRunAgainOnTheirOwn(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG, die_after: 1}}]", false)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	want := []string{"run batch=true marks=true leaked=false e2e/cart.spec.ts,e2e/pay.spec.ts", "run batch=false marks=false leaked=false e2e/pay.spec.ts"}
	if got := drivenLog(t, f.driverLog); !slices.Equal(got, want) {
		t.Fatalf("driver calls: %q", got)
	}

	if !strings.Contains(stderr, "left 1 of 2 slices without a result") {
		t.Fatalf("the batch's log doesn't say why it ran a slice again:\n%s", stderr)
	}
}

func TestARecordedStackBundleIsMarked(t *testing.T) {
	f := drivenRepo(t, "[{setup: stub, with: {log: LOG}}, {run: stub, batch: true, with: {log: LOG}}]", true)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	want := []string{"run batch=true marks=true leaked=false e2e/cart.spec.ts,e2e/pay.spec.ts", "setup stopped"}
	if got := drivenLog(t, f.driverLog); !slices.Equal(got, want) {
		t.Fatalf("driver calls: %q", got)
	}

	// Each line is "<action> <slice> <dir> <stack>", the slice empty at the setup's end.
	var collects []string

	for _, l := range drivenLog(t, f.correlatorLog) {
		if f := strings.SplitN(l, " ", 3); len(f) == 3 {
			collects = append(collects, strings.TrimSpace(f[0]+" "+f[1]))
		}
	}

	wantCollects := []string{
		"setup-end", "slice-start e2e/cart.spec.ts", "slice-end e2e/cart.spec.ts",
		"slice-start e2e/pay.spec.ts", "slice-end e2e/pay.spec.ts",
	}

	if !slices.Equal(collects, wantCollects) {
		t.Fatalf("collects: %q, want %q", collects, wantCollects)
	}

	if l := recordedLists(t, f.root); l["e2e/e2e/cart.spec.ts"] != "unit:e2e/cart.spec.ts" {
		t.Fatalf("lists: %v", l)
	}
}

func TestOnlyAMarkedBundleHasTheMarksDirectory(t *testing.T) {
	f := drivenRepo(t, "[{setup: stub, with: {log: LOG}}, {run: stub, batch: true, with: {log: LOG, die_after: 1, env: true}}]", true)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	got := drivenLog(t, f.driverLog)
	if len(got) != 5 {
		t.Fatalf("driver calls: %q", got)
	}

	dir, ok := strings.CutPrefix(got[1], "env marks=")
	if got[0] != "run batch=true marks=true leaked=false e2e/cart.spec.ts,e2e/pay.spec.ts" || !ok || dir == "" {
		t.Fatalf("the marked bundle: %q", got)
	}

	want := []string{"run batch=false marks=false leaked=false e2e/pay.spec.ts", "env marks=", "setup stopped"}
	if !slices.Equal(got[2:], want) {
		t.Fatalf("the slice run again alone: %q, want %q", got[2:], want)
	}

	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("the bundle's marks directory outlived it: %s", dir)
	}
}

func TestARecordedStackSuiteWithoutBatchHasNoMarks(t *testing.T) {
	f := drivenRepo(t, "[{setup: stub, with: {log: LOG}}, {run: stub, with: {log: LOG, env: true}}]", true)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	want := []string{
		"run batch=false marks=false leaked=false e2e/cart.spec.ts", "env marks=",
		"run batch=false marks=false leaked=false e2e/pay.spec.ts", "env marks=", "setup stopped",
	}
	if got := drivenLog(t, f.driverLog); !slices.Equal(got, want) {
		t.Fatalf("driver calls: %q, want %q", got, want)
	}
}

func edgesLog(t *testing.T, steps string) []string {
	t.Helper()

	f := drivenRepo(t, steps, false)

	// one log for both: the driver's lines and the correlator's, in order.
	if err := os.Symlink(f.correlatorLog, f.driverLog); err != nil {
		t.Fatal(err)
	}

	if stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root); code != 0 {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	var out []string

	for _, l := range drivenLog(t, f.correlatorLog) {
		switch fields := strings.Fields(l); {
		case len(fields) >= 2 && (fields[0] == "work" || fields[0] == "slice-start" || fields[0] == "slice-end"):
			out = append(out, fields[0]+" "+fields[1])
		case len(fields) >= 1 && fields[0] == "run":
			out = append(out, l)
		}
	}

	return out
}

func TestARecordingBundleCollectsAroundEachSlicesWork(t *testing.T) {
	got := edgesLog(t, "[{run: stub, batch: true, with: {log: LOG, work: true}}]")

	want := []string{
		"run batch=true marks=true leaked=false e2e/cart.spec.ts,e2e/pay.spec.ts",
		"slice-start e2e/cart.spec.ts", "work e2e/cart.spec.ts", "slice-end e2e/cart.spec.ts",
		"slice-start e2e/pay.spec.ts", "work e2e/pay.spec.ts", "slice-end e2e/pay.spec.ts",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the batch's order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestADriverThatDoesntMarkGetsNoCollectAfterTheFact(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG, work: true, deaf: true}}]", false)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code == 0 || !strings.Contains(stdout+stderr, "e2e/cart.spec.ts errored, e2e/pay.spec.ts errored") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	for _, l := range drivenLog(t, f.correlatorLog) {
		if strings.HasPrefix(l, "slice-start ") || strings.HasPrefix(l, "slice-end ") {
			t.Fatalf("collected for a slice that never marked: %q", l)
		}
	}
}

func TestASliceNoRunStepTakesIsRefused(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, take: [e2e/cart.spec.ts], with: {log: LOG}}]", false)

	stdout, stderr, code := runIn(t, f.root, f.env, "slice", "run", "--suite", "e2e", "--root", f.root)
	if code == 0 || !strings.Contains(stderr, "1 slice taken by no run step or by more than one: e2e/pay.spec.ts") {
		t.Fatalf("slice run: %d %s\n%s", code, stdout, stderr)
	}

	if _, err := os.Stat(f.driverLog); err == nil {
		t.Fatal("a driver ran")
	}
}

func TestSliceRunOnThisMachine(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	stdout, stderr, code := runIn(t, f.root, f.env, "slice", "run", "--suite", "e2e", "--root", f.root, "--slice", "e2e/cart.spec.ts")
	if code != 0 || strings.TrimSpace(stdout) != "slice e2e/cart.spec.ts: passed" {
		t.Fatalf("slice run: %d %q\n%s", code, stdout, stderr)
	}
}

func TestCheckRoutesEverySliceToOneRunStep(t *testing.T) {
	steps := `[{run: command, take: [e2e/cart.spec.ts], with: {each: ["true"]}},
	           {run: command, take: ["e2e/*"], with: {each: ["true"], bogus: 1}}]`
	f := drivenRepo(t, steps, false)

	cfg, err := config.Load(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	var problems []string
	for _, finding := range stepsFindings(t.Context(), cfg, f.root, filepath.Join(f.root, "tools", "plugins"), t.TempDir()) {
		problems = append(problems, finding.Problem)
	}

	text := strings.Join(problems, "\n")

	for _, want := range []string{
		"suite e2e: 1 slice more than one run step takes: e2e/cart.spec.ts",
		"suites.e2e.steps[1].with.bogus: unknown key; the keys of the command driver's settings are run, start, stop, each, all, dir, env, ready, export",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no finding %q in:\n%s", want, text)
		}
	}

	if strings.Contains(text, "pay.spec.ts") {
		t.Errorf("a slice one step takes was found:\n%s", text)
	}
}

func TestSliceMarkReturnsOnceTheBatchTookIt(t *testing.T) {
	dir := t.TempDir()
	done := make(chan struct{})

	go func() {
		defer close(done)

		for range 500 {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.json")) //nolint:errcheck // a fixed pattern
			for _, m := range matches {
				if err := os.WriteFile(strings.TrimSuffix(m, ".json")+".done", nil, 0o600); err == nil {
					return
				}
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdout, stderr, code := runIn(t, t.TempDir(), nil, "slice", "mark", "start", "e2e/cart.spec.ts", "--dir", dir)

	<-done

	if code != 0 || strings.TrimSpace(stdout) != "marked start e2e/cart.spec.ts" {
		t.Fatalf("mark: %d %q %s", code, stdout, stderr)
	}

	_, stderr, code = runIn(t, t.TempDir(), nil, "slice", "mark", "stop", "e2e/cart.spec.ts", "--dir", t.TempDir(), "--timeout", "200ms")
	if code == 0 || !strings.Contains(stderr, "did not answer") {
		t.Fatalf("an unanswered mark: %d %s", code, stderr)
	}
}

func TestTheCommandDriverRunsASuiteLikeTheExamplesSmoke(t *testing.T) {
	stopped := filepath.Join(t.TempDir(), "stopped")
	steps := `[{setup: command, with: {start: [sh, -c, "touch ready; trap 'touch ` + stopped + `; exit 0' TERM; while :; do sleep 1; done"],
	            ready: {file: ready, seconds: 30}, export: {STACK: up}}},
	           {run: command, with: {each: [sh, -c, 'test "$STACK" = up && test -f {source}']}}]`
	f := drivenRepo(t, steps, false)

	stdout, stderr, code := runIn(t, f.root, f.env, "slice", "run", "--suite", "e2e", "--root", f.root)
	if code != 0 || !strings.Contains(stdout, "slice e2e/cart.spec.ts: passed") || !strings.Contains(stdout, "slice e2e/pay.spec.ts: passed") {
		t.Fatalf("slice run: %d %s\n%s", code, stdout, stderr)
	}

	if _, err := os.Stat(stopped); err != nil {
		t.Fatalf("the set-up was not stopped: %v\n%s", err, stderr)
	}
}

func TestRecordGivesTheBatchWhatThePrepareMade(t *testing.T) {
	steps := `[{prepare: command, with: {run: [sh, -c, 'echo built > {prepared}/server']}},
	           {setup: command, with: {run: [test, -f, "{prepared}/server"]}},
	           {run: stub, batch: true, with: {log: LOG}}]`
	f := drivenRepo(t, steps, false)

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code != 0 || !strings.Contains(stdout, "recorded 2 slices of e2e") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}
}

func TestEveryDriverSeesTheRunsCommit(t *testing.T) {
	t.Setenv("PIPELINEGEN_CHANGE", "pr-12")
	t.Setenv(plugin.EnvCommit, "stale")

	f := drivenRepo(t, `[{run: stub, take: [e2e/cart.spec.ts], with: {log: LOG, commit: true}},
	                     {run: command, take: [e2e/pay.spec.ts], with: {each: [sh, -c, 'test "$PIPELINEGEN_COMMIT" = 0123abcd && test -z "$PIPELINEGEN_CHANGE"']}}]`, false)

	cfg, err := config.Load(filepath.Join(f.root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	b := &drivenBatch{
		cfg: cfg, suite: "e2e", tree: f.root, tools: filepath.Join(f.root, "tools"), plugins: filepath.Join(f.root, "tools", "plugins"),
		work: t.TempDir(), cache: t.TempDir(), commit: "0123abcd", log: io.Discard,
	}

	for i, st := range cfg.Suites["e2e"].Steps {
		rs, err := b.ready(t.Context(), st, nil)
		if err != nil {
			t.Fatal(err)
		}

		rs.req.Slices = []plugin.DriverSlice{{Name: st.Take[0], Sources: []string{st.Take[0]}, Extras: t.TempDir()}}

		var ends []plugin.DriverEvent

		if tail, err := rs.d.Run(t.Context(), rs.req, func(ev plugin.DriverEvent) {
			if ev.Event == plugin.EventEnd {
				ends = append(ends, ev)
			}
		}); err != nil || len(ends) != 1 || !ends[0].Passed {
			t.Errorf("steps[%d]: %v %+v\n%s", i, err, ends, tail)
		}
	}

	if got := drivenLog(t, f.driverLog); !slices.Contains(got, "commit=0123abcd change=") {
		t.Errorf("the plugin's call saw: %q", got)
	}
}
