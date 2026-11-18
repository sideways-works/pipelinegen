package kit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// stepReaderFakes is a correlator that reads its build_root relative to its working directory and refuses
// to run where that holds more than the tree and coverage output, as only the pipeline's step leaves it.
func stepReaderFakes() map[string]plugin.Handler {
	h := fakes("correlator")
	d := plugin.Description{Kind: plugin.KindCorrelator, Protocol: plugin.Protocol, Name: "stepreader", Version: "1",
		Capabilities: []string{plugin.CallLinks, plugin.CallObserve, plugin.CallCollect},
		Collects:     []string{plugin.CollectSliceEnd}, ObserveReads: []string{"build_root"}}
	h[plugin.CallDescribe] = func([]byte) (any, error) { return d, nil }

	observe := h[plugin.CallObserve]
	h[plugin.CallObserve] = func(in []byte) (any, error) {
		entries, err := os.ReadDir(".")
		if err != nil {
			return nil, fmt.Errorf("observe: %w", err)
		}

		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}

		if !slices.Equal(names, []string{plugin.ObserveCoverage, plugin.ObserveTree}) {
			return nil, fmt.Errorf("%w: observe ran beside %v", errUndeclared, names)
		}

		if _, err := os.Stat(plugin.ObserveTree + "/."); err != nil {
			return nil, fmt.Errorf("observe: the tree: %w", err)
		}

		return observe(in)
	}

	return h
}

func TestTheKitObservesAsTheStepThatObserves(t *testing.T) {
	const reads = "observe reads only what the step that observes sees (the tree and the coverage output)"

	for _, tc := range []struct {
		buildRoot string
		bad       bool
	}{
		{"", false},
		{"source/dist", false},
		{"coverage/build", false},
		{"prepared/tree", true},
		{"/srv/build", true},
		{"source/../prepared", true},
	} {
		t.Run(tc.buildRoot, func(t *testing.T) {
			o := fakeOptions(t, "stepreader", "", true, true)
			if tc.buildRoot != "" {
				o.Config = map[string]any{"build_root": tc.buildRoot}
			}

			results := map[string]string{}

			for _, r := range Run(t.Context(), o) {
				if !r.Skipped {
					results[r.Check] = r.Problem
				}
			}

			if p, ran := results[reads]; !ran || (p != "") != tc.bad || tc.bad && !strings.Contains(p, tc.buildRoot) {
				t.Fatalf("%q: %q (ran %t)", tc.buildRoot, p, ran)
			}

			for check, p := range results {
				if check != reads && p != "" {
					t.Errorf("%s: %s", check, p)
				}
			}
		})
	}
}

// executorFakes is an sbom that, asked for units, runs the tree's Makefile
// line as a build would.
func executorFakes() map[string]plugin.Handler {
	h := fakes("sbom")
	units := h[plugin.CallUnits]

	h[plugin.CallUnits] = func(in []byte) (any, error) {
		var req plugin.TreeRequest
		if err := decode(in, &req); err != nil {
			return nil, err
		}

		if data, err := os.ReadFile(filepath.Join(req.Tree, markerDir, "Makefile")); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if cmd, ok := strings.CutPrefix(line, "\t"); ok {
					if out, err := exec.CommandContext(context.Background(), "sh", "-c", cmd).CombinedOutput(); err != nil {
						return nil, fmt.Errorf("%w: %s", err, out)
					}
				}
			}
		}

		return units(in)
	}

	return h
}

func TestAPluginThatRunsTheTreeFails(t *testing.T) {
	results := runFake(t, "executor")
	if p := results[neverExecCheck]; !strings.Contains(p, "Makefile") {
		t.Fatalf("%q", p)
	}

	if p, ran := runFake(t, "sbom")[neverExecCheck]; !ran || p != "" {
		t.Fatalf("a well-behaved sbom: %q (ran %t)", p, ran)
	}
}

func TestACollectorThatIgnoresAPartsDirectoryFails(t *testing.T) {
	if p := runFake(t, "onepart")[partsCheck]; !strings.Contains(p, "part 2 left nothing in coverage/.pipelinegen/part-2/s/fake") {
		t.Fatalf("%q", p)
	}

	if p, ran := runFake(t, "correlator")[partsCheck]; !ran || p != "" {
		t.Fatalf("a well-behaved collector: %q (ran %t)", p, ran)
	}
}

func TestThePartsCheckRunsAsTheBatchesStep(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	o := fakeOptions(t, "modes", "", true, true)

	kitJSON := `{"observe": {"slice": "s", "dirs": ["rec/setup", "rec/s"]}, "name": "entry", "record": [` +
		`{"slice": "s", "run": ["true"], "config": {"log": "` + log + `", "mode": "stack"}},` +
		`{"slice": "s", "run": ["true"], "stack": false, "config": {"log": "` + log + `", "mode": "plain"}}]}`
	if err := os.WriteFile(filepath.Join(o.Tree, kitFile), []byte(kitJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, r := range Run(t.Context(), o) {
		if r.Check == partsCheck && r.Problem != "" {
			t.Fatalf("%s", r.Problem)
		}
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	want := "true entry true stack\ntrue entry true stack\ntrue entry false plain\ntrue entry false plain\n"
	if string(data) != want {
		t.Fatalf("collect ran as\n%s", data)
	}
}

func TestThePartsCheckRunsTheBatchesStack(t *testing.T) {
	for name, tc := range map[string]struct {
		serve, ready, want string
	}{
		"well behaved":   {`echo $$ > coverage/stack.pid; touch coverage/stack.ready; exec sleep 600`, "coverage/*.ready", ""},
		"no ready file":  {`exec sleep 600`, "", "names its ready file"},
		"exits early":    {`touch coverage/stack.ready`, "coverage/*.ready", "exited before the last collect"},
		"leaves a child": {`sleep 600 & touch coverage/stack.ready; wait`, "coverage/*.ready", "left a process of its group running"},
	} {
		t.Run(name, func(t *testing.T) {
			o := fakeOptions(t, "correlator", "", true, true)

			ready := ""
			if tc.ready != "" {
				ready = `, "ready": "` + tc.ready + `"`
			}

			kitJSON := `{"observe": {"slice": "s", "dirs": ["rec/setup", "rec/s"]}, "record": {"slice": "s", "run": ["true"], ` +
				`"serve": ["sh", "-c", "` + strings.ReplaceAll(tc.serve, `"`, `\"`) + `"]` + ready + `}}`
			if err := os.WriteFile(filepath.Join(o.Tree, kitFile), []byte(kitJSON), 0o600); err != nil {
				t.Fatal(err)
			}

			problem := ""

			for _, r := range Run(t.Context(), o) {
				if r.Check == partsCheck {
					problem = r.Problem
				}
			}

			if tc.want == "" && problem != "" || tc.want != "" && !strings.Contains(problem, tc.want) {
				t.Fatalf("%q, want %q", problem, tc.want)
			}

			if pids, _ := filepath.Glob(filepath.Join(o.Work, "*", "coverage", "stack.pid")); len(pids) > 0 { //nolint:errcheck // a fixed pattern
				data, _ := os.ReadFile(pids[0]) //nolint:errcheck // checked below
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && syscall.Kill(pid, 0) == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck // the test's own leftover

					t.Fatal("the stack outlived the check")
				}
			}
		})
	}
}

func eraserFakes(both bool) map[string]plugin.Handler {
	h := recorderFakes(false)
	h[plugin.CallDetect] = func([]byte) (any, error) {
		return plugin.DetectAnswer{Claims: []plugin.Claim{{Path: "a.txt", Role: "source", Erased: true, Recorded: both}}}, nil
	}

	return h
}

func execSBOMFakes() map[string]plugin.Handler {
	h := fakes("sbom")

	h[plugin.CallUnits] = func(in []byte) (any, error) {
		var req plugin.TreeRequest
		if err := decode(in, &req); err != nil {
			return nil, err
		}

		var a plugin.UnitsAnswer

		for _, p := range req.Paths {
			a.Units = append(a.Units, plugin.Unit{ID: "t:" + p + "#run", File: p, Digest: "1", Executable: true})
		}

		return a, nil
	}

	return h
}

// recorderFakes is the fake correlator that also detects: it claims
// a.txt, or b.txt when bad, as a file its recordings hold.
func recorderFakes(bad bool) map[string]plugin.Handler {
	h := fakes("correlator")
	d := plugin.Description{Kind: plugin.KindCorrelator, Protocol: plugin.Protocol, Name: "recorder", Version: "1",
		Capabilities: []string{plugin.CallDetect, plugin.CallLinks, plugin.CallObserve, plugin.CallCollect},
		Candidates:   []string{"*.txt"}, Collects: []string{plugin.CollectSliceEnd}}
	h[plugin.CallDescribe] = func([]byte) (any, error) { return d, nil }

	file := "a.txt"
	if bad {
		file = "b.txt"
	}

	h[plugin.CallDetect] = func([]byte) (any, error) {
		return plugin.DetectAnswer{Claims: []plugin.Claim{{Path: file, Role: "source", Recorded: true}}}, nil
	}

	return h
}

func TestAClaimMarkedRecordedIsAFileTheRecordingHolds(t *testing.T) {
	const check = "a claim marked recorded is a file the recording holds"

	if p, ran := runFake(t, "recorder")[check]; !ran || p != "" {
		t.Fatalf("a file the recording holds: %q (ran %t)", p, ran)
	}

	if p := runFake(t, "recorderbad")[check]; !strings.Contains(p, "b.txt is marked recorded, and the fixture's recording holds no unit of it") {
		t.Fatalf("a file it doesn't: %q", p)
	}
}

func TestAClaimMarkedErasedHoldsNothingThatRuns(t *testing.T) {
	const check = "a claim marked recorded is a file the recording holds"

	results := func(kind, sbom string) map[string]string {
		out := map[string]string{}

		for _, r := range runFakeBeside(t, kind, sbom, true, true) {
			if !r.Skipped {
				out[r.Check] = r.Problem
			}
		}

		return out
	}

	if p := results("eraser", "sbom")[check]; p != "" {
		t.Fatalf("types only: %q", p)
	}

	if p := results("eraser", "execsbom")[check]; !strings.Contains(p, "a.txt is marked erased, and the sbom answers") {
		t.Fatalf("a unit that runs: %q", p)
	}

	if p := results("eraser", "")[check]; !strings.Contains(p, "no companion sbom") {
		t.Fatalf("no companion: %q", p)
	}

	if p := results("eraserboth", "sbom")[check]; !strings.Contains(p, "a.txt is marked both recorded and erased") {
		t.Fatalf("both: %q", p)
	}
}
