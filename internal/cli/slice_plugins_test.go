package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/connectortest"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// TestMain doubles as the fake correlators: with FAKE set, the test binary
// serves the call it was started with.
func TestMain(m *testing.M) {
	// the connectors download from a server of the test's, as a release
	if _, err := connectortest.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// plugins missing from a test's plugins directory are fetched from the test's releases, never GitHub
	if os.Getenv(envReleasesURL) == "" {
		os.Setenv(envReleasesURL, serveReleases()+"/%s/releases/download/%s/%s") //nolint:errcheck // for the commands the tests start
	}

	if kind := os.Getenv("FAKE_PLUGIN"); kind != "" {
		testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, fakePlugin(kind)))
	}

	// a command run in a process of its own, with its own environment and
	// working directory, so tests that run commands can run side by side.
	if os.Getenv(envTestCommand) != "" {
		testenv.ExitChild(m, Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}

	if os.Getenv("FAKE") != "" {
		testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, fakeCorrelator()))
	}

	if os.Getenv("FAKE_DRIVER") != "" {
		h := fakeDriver()
		if os.Getenv("FAKE_VALIDATES") != "" {
			h.Validate = fakeValidate
		}

		testenv.ExitChild(m, plugin.ServeDriver(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, h))
	}

	// the Bazel servers the tests start get an output root of the test
	// binary's own, removed when they end.
	os.Exit(testenv.Bazel(m))
}

var errFake = errors.New("the fake was told to fail")

// fakeCorrelator observes touched.txt and setup_like.txt in each directory, one id per line; a directory
// without either is no evidence.
func fakeCorrelator() map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: func(in []byte) (any, error) {
			var req plugin.Request
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			if err := note(req.Config, "describe"); err != nil {
				return nil, err
			}

			d := plugin.Description{Protocol: plugin.Protocol, Kind: plugin.KindCorrelator, Name: "fake", Version: "v0",
				Capabilities: []string{plugin.CallCollect, plugin.CallObserve}, ObserveReads: []string{"build_root"}}

			if collects, ok := req.Config["collects"].([]any); ok {
				for _, c := range collects {
					d.Collects = append(d.Collects, fmt.Sprint(c))
				}
			}

			return d, nil
		},
		plugin.CallCollect: func(in []byte) (any, error) {
			var req plugin.CollectRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			if err := note(req.Config, fmt.Sprintf("%s %s %s %t", req.Action, req.Slice, filepath.ToSlash(req.Dir), req.Stack)); err != nil {
				return nil, err
			}

			if req.Config["fail"] == req.Action {
				return nil, errFake
			}

			if req.Config["sleep"] == req.Action {
				time.Sleep(10 * time.Second)
			}

			return struct{}{}, nil
		},
		plugin.CallObserve: func(in []byte) (any, error) {
			var req plugin.ObserveRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			if req.Config["fail"] == plugin.CallObserve {
				return nil, errFake
			}

			var a plugin.ObserveAnswer

			for i, dir := range req.Dirs {
				touched, okT := readIDs(filepath.Join(dir, "touched.txt"))
				setup, okS := readIDs(filepath.Join(dir, "setup_like.txt"))

				if i > 0 && !okT && !okS {
					return nil, plugin.ErrCantTell
				}

				a.Touched = append(a.Touched, touched...)
				a.SetupLike = append(a.SetupLike, setup...)
			}

			if a.Touched == nil {
				a.Touched = []string{}
			}

			return a, nil
		},
	}
}

func note(config map[string]any, line string) error {
	log, ok := config["log"].(string)
	if !ok {
		return nil
	}

	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}

	_, werr := fmt.Fprintln(f, line)
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("log: %w", err)
	}

	return nil
}

func readIDs(p string) ([]string, bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}

	return strings.Fields(string(data)), true
}

const envTestCommand = "PIPELINEGEN_TEST_COMMAND"

func runIn(t *testing.T, dir string, env map[string]string, args ...string) (string, string, int) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), exe, args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), envTestCommand+"=1")

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}

		return stdout.String(), stderr.String(), exit.ExitCode()
	}

	return stdout.String(), stderr.String(), 0
}

type pluginBatch struct {
	root  string
	store *gitstore.Store
	id    report.ID
	dir   string
	env   map[string]string
}

func newPluginBatch(t *testing.T, stack bool, correlators map[string]string) *pluginBatch {
	t.Helper()

	root, _, s, env := runEnvFor(t)

	suite := "    tag: slice-e2e\n    bind: recorded\n"
	if stack {
		suite += "    stack: true\n"
	}

	cfg := strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", suite, 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}}\n    record: true\n", 1) + "plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n"

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	names := make([]string, 0, len(correlators))
	for name := range correlators {
		names = append(names, name)
	}

	slices.Sort(names)

	var conf strings.Builder

	conf.WriteString(cfg)

	for _, name := range names {
		fmt.Fprintf(&conf, "  - {name: %s, kind: correlator, repository: example/%s, commit: 0123456789abcdef0123456789abcdef01234567, config: %s}\n",
			name, name, correlators[name])
		writeFile(t, filepath.Join(dir, "tools/plugins", name), "#!/bin/sh\nFAKE=1 exec '"+exe+"' \"$@\"\n")

		if err := os.Chmod(filepath.Join(dir, "tools/plugins", name), 0o700); err != nil { //nolint:gosec // the test's own plugin
			t.Fatal(err)
		}
	}

	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), conf.String())

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "batches/1/slices.json"), `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
	writeFile(t, filepath.Join(dir, "result/e2e.a.json"), `{"passed": true}`)
	writeFile(t, filepath.Join(dir, "result/e2e.b.json"), `{"passed": true}`)

	return &pluginBatch{root: root, store: s, id: id, dir: dir, env: env}
}

func (b *pluginBatch) step(t *testing.T, args ...string) string {
	t.Helper()

	stdout, stderr, code := b.run(t, append(append([]string{"slice"}, args...), "-root", b.root)...)
	if code != 0 {
		t.Fatalf("slice %v: %d %s", args, code, stderr)
	}

	return stdout
}

func (b *pluginBatch) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	return runIn(t, b.dir, b.env, args...)
}

func (b *pluginBatch) leave(t *testing.T, part, plugin, file string, ids ...string) {
	t.Helper()

	writeFile(t, filepath.Join(b.dir, "coverage", part, plugin, file), strings.Join(ids, "\n")+"\n")
}

func (b *pluginBatch) state(t *testing.T, slice string) report.StateFile {
	t.Helper()

	out := t.TempDir()
	if _, err := report.Collect(t.Context(), b.store, b.id, "e2e", out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(out, slice, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	var sf report.StateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatal(err)
	}

	return sf
}

// logLines takes the batch's directory out of the paths, as the command makes its path flags absolute.
func logLines(t *testing.T, b *pluginBatch, p string) []string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	wd := b.dir

	text := strings.ReplaceAll(string(data), filepath.ToSlash(wd)+"/", "")

	// the collect calls; describes are counted on their own.
	return slices.DeleteFunc(strings.Split(strings.TrimSpace(text), "\n"), func(l string) bool { return l == "describe" })
}

func describes(t *testing.T, p string) int {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return strings.Count(string(data), "describe\n")
}

func TestCollectFollowsTheBatchInOrder(t *testing.T) {
	t.Parallel()

	logs := t.TempDir()
	b := newPluginBatch(t, true, map[string]string{
		"one": "{log: " + filepath.Join(logs, "one") + "}",
		"two": "{log: " + filepath.Join(logs, "two") + "}",
	})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.step(t, "start", "e2e.b")
	b.step(t, "start", "e2e.a")
	b.step(t, "end", "e2e.b")
	b.step(t, "end", "e2e.a")

	for _, name := range []string{"one", "two"} {
		want := []string{
			"setup-end  coverage/_setup/" + name + " true",
			"slice-start e2e.a coverage/e2e.a/" + name + " true",
			"slice-end e2e.a coverage/e2e.a/" + name + " true",
			"slice-start e2e.b coverage/e2e.b/" + name + " true",
			"slice-end e2e.b coverage/e2e.b/" + name + " true",
			"slice-start e2e.a coverage/.pipelinegen/part-2/e2e.a/" + name + " true",
			"slice-end e2e.a coverage/.pipelinegen/part-2/e2e.a/" + name + " true",
		}

		if got := logLines(t, b, filepath.Join(logs, name)); !slices.Equal(got, want) {
			t.Errorf("%s collected\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

func TestAFailingCollectorErrorsTheSliceAndNamesItself(t *testing.T) {
	t.Parallel()

	logs := t.TempDir()
	b := newPluginBatch(t, false, map[string]string{
		"broken": "{log: " + filepath.Join(logs, "broken") + ", fail: slice-start}",
		"fine":   "{log: " + filepath.Join(logs, "fine") + "}",
	})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")

	if out := b.step(t, "end", "e2e.a"); out != "slice e2e.a: passed\n" {
		t.Fatalf("end: %q", out)
	}

	b.step(t, "flush")

	sf := b.state(t, "e2e.a")
	if sf.State != report.Errored || !slices.ContainsFunc(sf.Failures, func(f report.Failure) bool {
		return f.Test == "(recording)" && strings.Contains(f.Tail, "broken collect")
	}) {
		t.Fatalf("state %+v", sf)
	}

	b.step(t, "start", "e2e.b")
	b.step(t, "end", "e2e.b")

	want := []string{
		"setup-end  coverage/_setup/fine false",
		"slice-start e2e.a coverage/e2e.a/fine false",
		"slice-end e2e.a coverage/e2e.a/fine false",
		"slice-start e2e.b coverage/e2e.b/fine false",
		"slice-end e2e.b coverage/e2e.b/fine false",
	}
	if got := logLines(t, b, filepath.Join(logs, "fine")); !slices.Equal(got, want) {
		t.Fatalf("fine collected %v", got)
	}

	if got := logLines(t, b, filepath.Join(logs, "broken")); len(got) != 5 {
		t.Fatalf("broken was asked %v", got)
	}

	b = newPluginBatch(t, false, map[string]string{"broken": "{fail: setup-end}"})
	if _, stderr, code := b.run(t, "slice", "setup-end", "-root", b.root); code == 0 || !strings.Contains(stderr, "broken collect") {
		t.Fatalf("setup-end: %d %s", code, stderr)
	}
}

func TestACollectStopsBeforeTheBatchsLimit(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, false, map[string]string{"slow": "{sleep: slice-end}"})

	data, err := os.ReadFile(filepath.Join(b.root, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	pl, err := loadCorrelators(t.Context(), cfg, "e2e", filepath.Join(b.dir, "tools"), "")
	if err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	cs := pl.collectors(cfg, "e2e", began.Add(collectMargin+time.Second))

	err = cs[0].Collect(t.Context(), plugin.CollectSliceEnd, "e2e.a", filepath.Join(b.dir, "coverage", "e2e.a"))
	if err == nil || !strings.Contains(err.Error(), "slow collect (slice-end of e2e.a): stopped after 1s, at the batch's time limit") {
		t.Errorf("got %v", err)
	}

	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("stopped after %s", took)
	}
}

func TestAStackWithoutACollectingCorrelatorFailsAtStart(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, true, nil)

	if _, stderr, code := b.run(t, "slice", "start", "e2e.a", "-root", b.root); code != 1 ||
		!strings.Contains(stderr, "suite e2e runs a stack (stack: true), but no configured correlator collects") {
		t.Fatalf("start: %d %s", code, stderr)
	}
}

func (b *pluginBatch) recorded(t *testing.T, slice string) recording.Recording {
	t.Helper()

	out := t.TempDir()
	if _, err := report.Collect(t.Context(), b.store, b.id, "e2e", out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(out, slice, "recording.txt"))
	if err != nil {
		t.Fatal(err)
	}

	rec, err := recording.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	return rec
}

func TestARecordingIsWhatEveryCorrelatorObserved(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, false, map[string]string{"go": "{}", "sql": "{}"})

	b.step(t, "setup-end")
	b.leave(t, "_setup", "go", "touched.txt", "go:server#func:Start")
	b.leave(t, "_setup", "go", "setup_like.txt", "go:server#func:Start")

	b.step(t, "start", "e2e.a")
	b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")
	b.leave(t, "e2e.a", "sql", "touched.txt", "pg:table:public.notes")
	b.step(t, "start", "e2e.b")
	b.leave(t, "e2e.b", "go", "touched.txt", "go:notes#func:List")
	b.leave(t, "e2e.b", "sql", "touched.txt")
	b.step(t, "start", "e2e.a")
	b.leave(t, ".pipelinegen/part-2/e2e.a", "go", "touched.txt", "go:notes#func:Delete")
	b.leave(t, ".pipelinegen/part-2/e2e.a", "sql", "touched.txt")
	b.step(t, "end", "e2e.b")
	b.step(t, "end", "e2e.a")
	b.step(t, "flush")

	got := b.recorded(t, "e2e.a")
	if !slices.Equal(got.Own, []string{"go:notes#func:Create", "go:notes#func:Delete", "pg:table:public.notes"}) ||
		!slices.Equal(got.Setup, []string{"go:server#func:Start"}) || !got.Observed || len(got.CannotTell) > 0 {
		t.Fatalf("a recorded %+v", got)
	}

	if sf := b.state(t, "e2e.a"); sf.State != report.Passed || !sf.Recorded {
		t.Fatalf("a: %+v", sf)
	}
}

func TestACorrelatorThatCantTellMarksTheSlice(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, false, map[string]string{"go": "{}", "sql": "{}"})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")
	b.step(t, "end", "e2e.a")
	b.step(t, "start", "e2e.b")
	b.leave(t, "e2e.b", "go", "touched.txt", "go:notes#func:List")
	b.leave(t, "e2e.b", "sql", "touched.txt")
	b.step(t, "end", "e2e.b")
	b.step(t, "flush")

	got := b.recorded(t, "e2e.a")
	if got.HasData() || len(got.Own) > 0 || len(got.CannotTell) != 1 || got.CannotTell[0].Plugin != "sql" {
		t.Fatalf("a recorded %+v", got)
	}

	if sf := b.state(t, "e2e.a"); sf.State != report.Passed || len(sf.CannotTell) != 1 {
		t.Fatalf("a: %+v", sf)
	}

	if got := b.recorded(t, "e2e.b"); !got.HasData() || !slices.Equal(got.Own, []string{"go:notes#func:List"}) {
		t.Fatalf("b recorded %+v", got)
	}
}

func TestAFailedObserveErrorsTheSlice(t *testing.T) {
	t.Parallel()

	// data the correlator can't read is no "can't tell"
	b := newPluginBatch(t, false, map[string]string{"go": "{fail: observe}"})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")
	b.step(t, "end", "e2e.a")
	b.step(t, "flush")

	if sf := b.state(t, "e2e.a"); sf.State != report.Errored ||
		!slices.ContainsFunc(sf.Failures, func(f report.Failure) bool { return strings.Contains(f.Tail, "go observe") }) {
		t.Fatalf("a: %+v", sf)
	}
}

func TestABatchDescribesItsCorrelatorsOnce(t *testing.T) {
	t.Parallel()

	logs := t.TempDir()
	b := newPluginBatch(t, true, map[string]string{
		"one": "{log: " + filepath.Join(logs, "one") + "}",
		"two": "{log: " + filepath.Join(logs, "two") + "}",
	})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.step(t, "start", "e2e.b")
	b.step(t, "end", "e2e.b")
	b.step(t, "end", "e2e.a")
	b.step(t, "flush")

	for _, name := range []string{"one", "two"} {
		if n := describes(t, filepath.Join(logs, name)); n != 1 {
			t.Errorf("%s described itself %d times", name, n)
		}
	}

	writeFile(t, filepath.Join(b.dir, "result/.pipelinegen/described.json"), "{")
	b.step(t, "flush")

	if n := describes(t, filepath.Join(logs, "one")); n != 2 {
		t.Errorf("after a broken file: %d describes", n)
	}
}

func TestCollectIsCalledOnlyForTheActionsACorrelatorHandles(t *testing.T) {
	t.Parallel()

	logs := t.TempDir()
	b := newPluginBatch(t, true, map[string]string{
		"ends": "{log: " + filepath.Join(logs, "ends") + ", collects: [slice-end]}",
		"all":  "{log: " + filepath.Join(logs, "all") + "}",
	})

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.step(t, "start", "e2e.b")
	b.step(t, "end", "e2e.b")

	want := []string{
		"slice-end e2e.a coverage/e2e.a/ends true",
		"slice-end e2e.b coverage/e2e.b/ends true",
	}
	if got := logLines(t, b, filepath.Join(logs, "ends")); !slices.Equal(got, want) {
		t.Fatalf("ends collected %v", got)
	}

	if got := logLines(t, b, filepath.Join(logs, "all")); len(got) != 5 {
		t.Fatalf("all collected %v", got)
	}
}

func TestABatchWithoutATokenSaysSoOnce(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, false, map[string]string{"go": "{}"})
	token := b.env[envToken]
	b.env[envToken] = ""

	_, stderr, code := b.run(t, "slice", "setup-end", "-root", b.root)
	if code != 0 || strings.Count(stderr, "no "+envToken) != 1 {
		t.Fatalf("setup-end: %d %q", code, stderr)
	}

	b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")

	for _, step := range [][]string{{"start", "e2e.a"}, {"end", "e2e.a"}, {"flush"}} {
		if _, stderr, code := b.run(t, append(append([]string{"slice"}, step...), "-root", b.root)...); code != 0 || stderr != "" {
			t.Fatalf("slice %v: %d %q", step, code, stderr)
		}
	}

	before := t.TempDir()
	if _, err := report.Collect(t.Context(), b.store, b.id, "e2e", before); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(before, "e2e.a", "recording.txt")); err == nil {
		t.Fatal("a step without a token wrote the slice")
	}

	b.env[envToken] = token
	if _, stderr, code := b.run(t, "report", "batch-end", "-batch", "1", "-root", b.root); code != 0 && !strings.Contains(stderr, "did not pass") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if got := b.recorded(t, "e2e.a"); !slices.Equal(got.Own, []string{"go:notes#func:Create"}) {
		t.Fatalf("the batch's end didn't write the slice: %+v", got)
	}
}

func TestASuiteObservedByItsCorrelatorsOnly(t *testing.T) {
	t.Parallel()

	logs := t.TempDir()
	b := newPluginBatch(t, false, map[string]string{
		"go":  "{log: " + filepath.Join(logs, "go") + "}",
		"sql": "{log: " + filepath.Join(logs, "sql") + "}",
	})

	conf := readText(t, filepath.Join(b.root, "pipelinegen.yaml"))
	listed := strings.Replace(conf, "    bind: recorded\n", "    bind: recorded\n    correlators: [go]\n", 1)

	if listed == conf {
		t.Fatal("nothing replaced")
	}

	writeFile(t, filepath.Join(b.root, "pipelinegen.yaml"), listed)

	b.step(t, "setup-end")
	b.step(t, "start", "e2e.a")
	b.leave(t, "e2e.a", "go", "touched.txt", "go:notes#func:Create")
	b.step(t, "end", "e2e.a")
	b.step(t, "flush")

	if got := b.recorded(t, "e2e.a"); len(got.CannotTell) > 0 || !slices.Equal(got.Own, []string{"go:notes#func:Create"}) {
		t.Fatalf("a recorded %+v", got)
	}

	if _, err := os.Stat(filepath.Join(logs, "sql")); !os.IsNotExist(err) {
		t.Fatalf("sql was asked: %v", err)
	}

	cfg, err := config.Load(filepath.Join(b.root, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.CorrelatorsFor("e2e"); !slices.Equal(got, []string{"go"}) {
		t.Fatalf("observed by %v", got)
	}
}

func TestASliceOfABatchWhoseSetupFailedNeverPasses(t *testing.T) {
	t.Parallel()

	b := newPluginBatch(t, false, map[string]string{"broken": "{fail: setup-end}"})

	if _, _, code := b.run(t, "slice", "setup-end", "-root", b.root); code == 0 {
		t.Fatal("the setup ended though its collector failed")
	}

	for _, step := range []string{"start", "end"} {
		stdout, stderr, code := b.run(t, "slice", step, "e2e.a", "-root", b.root)
		if code == 0 || strings.Contains(stdout, "passed") || !strings.Contains(stderr, "the batch's setup hasn't ended") {
			t.Fatalf("slice %s after a failed setup: %d %q %s", step, code, stdout, stderr)
		}
	}

	if _, stderr, code := b.run(t, "report", "batch-end", "-root", b.root); code != 1 || !strings.Contains(stderr, "e2e.a errored") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if st := b.state(t, "e2e.a"); st.State != report.Errored {
		t.Fatalf("the slice's state: %+v", st)
	}
}
