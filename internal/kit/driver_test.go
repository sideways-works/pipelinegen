package kit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeDriver records each slice's name and fails it when the name holds "bad": "honest" bundled or not
// alike, "lenient" passes a bundle, "preparing"/"idle" prepare with/without a file, "together" never apart.
func fakeDriver(kind string) plugin.DriverHandlers {
	verbs := []string{plugin.CallRun}
	if kind == "preparing" || kind == "idle" {
		verbs = append(verbs, plugin.CallPrepare)
	}

	return plugin.DriverHandlers{
		Validate: fakeValidate(kind),
		Describe: func() (plugin.Description, error) {
			return plugin.Description{
				Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "fake-driver", Version: "v0",
				Verbs: verbs, Routes: &plugin.Routes{Files: []string{"**"}}, Apart: kind != "together",
			}, nil
		},
		Prepare: func(_ context.Context, req plugin.DriverRequest) error {
			if kind != "preparing" {
				return nil
			}

			if err := os.WriteFile(filepath.Join(req.Prepared, "built"), []byte("built\n"), 0o600); err != nil {
				return fmt.Errorf("prepare: %w", err)
			}

			return nil
		},
		Run: func(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error) error {
			// the strict kinds read their settings as they validate them;
			// the two-faced one runs whatever it gets
			if kind == "strict" || kind == "lax" {
				if err := validateFake(plugin.CallRun, req.Config); err != nil {
					return err
				}
			}

			for _, sl := range req.Slices {
				if err := fakeRun(ctx, kind, req, sl, emit); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

type fakeSettings struct {
	Mode string `json:"mode"`
}

func validateFake(_ string, settings map[string]any) error {
	var s fakeSettings

	return plugin.DecodeSettings(settings, &s)
}

// fakeValidate is a kind's validate: the strict and the two-faced kinds
// refuse what the settings don't name, the lax one takes anything.
func fakeValidate(kind string) func(string, map[string]any) error {
	switch kind {
	case "strict", "two-faced":
		return validateFake
	case "lax":
		return func(string, map[string]any) error { return nil }
	default:
		return nil
	}
}

func fakeRun(ctx context.Context, kind string, req plugin.DriverRequest, sl plugin.DriverSlice, emit func(plugin.DriverEvent) error) error {
	mark := func(what string) error {
		if req.Marks == nil {
			return nil
		}

		return driver.MarkAndWait(ctx, req.Marks.Dir, driver.Mark{Mark: what, Slice: sl.Name}, 30*time.Second)
	}

	if err := mark(plugin.MarkStart); err != nil {
		return err
	}

	if req.Record {
		if err := os.MkdirAll(sl.Recording, 0o750); err != nil {
			return fmt.Errorf("recording: %w", err)
		}

		if err := os.WriteFile(filepath.Join(sl.Recording, "ran.txt"), []byte("ran "+sl.Name+"\n"), 0o600); err != nil {
			return fmt.Errorf("recording: %w", err)
		}
	}

	if err := mark(plugin.MarkStop); err != nil {
		return err
	}

	one := 1
	passed := !strings.Contains(sl.Name, "bad") || kind == "lenient" && req.Batch && len(req.Slices) > 1

	ev := plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: passed, Tests: &one}
	if !passed {
		ev.Failures = []plugin.Failure{{Test: "it", Tail: "broke"}}
	}

	return emit(ev)
}

func driverOptions(t *testing.T, kind string) Options {
	t.Helper()

	return driverOptionsWith(t, kind, `{"driver": {"slices": [{"name": "a_test"}, {"name": "bad_test"}], "record": true}}`)
}

func driverOptionsWith(t *testing.T, kind, fixture string) Options {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	self := filepath.Join(t.TempDir(), "fake-driver")
	if err := testenv.WriteExecutable(t.Context(), self, []byte("#!/bin/sh\nKIT_DRIVER="+kind+" exec '"+exe+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	tree := t.TempDir()

	if err := os.WriteFile(filepath.Join(tree, kitFile), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	return Options{Path: self, Tree: tree, Work: t.TempDir()}
}

func failedChecks(results []Result) []string {
	var out []string

	for _, r := range results {
		if r.Problem != "" && !r.Skipped && !r.Note {
			out = append(out, r.Check+": "+r.Problem)
		}
	}

	return out
}

func TestAnHonestDriverPassesBothModesAndMarks(t *testing.T) {
	results := Run(t.Context(), driverOptions(t, "honest"))

	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}

	var ran []string
	for _, r := range results {
		ran = append(ran, r.Check)
	}

	for _, want := range []string{
		"one by one and bundled give each slice the same result, not recording",
		"one by one and bundled leave each slice the same recording",
		"a recorded stack suite's bundle marks each slice's start and stop, one slice at a time",
		"a recorded bundle of a suite with correlators marks each slice's start and stop, one slice at a time",
	} {
		if !strings.Contains(strings.Join(ran, "\n"), want) {
			t.Errorf("no check %q in %q", want, ran)
		}
	}
}

func TestADriverWhoseBundleSaysOtherwiseFails(t *testing.T) {
	failed := strings.Join(failedChecks(Run(t.Context(), driverOptions(t, "lenient"))), "\n")

	if !strings.Contains(failed, "one by one and bundled give each slice the same result") || !strings.Contains(failed, "bad_test: on its own passed false") {
		t.Fatalf("failed:\n%s", failed)
	}
}

func TestTheKitPreparesWithTheFixturesSettings(t *testing.T) {
	const name = "prepare fills the prepared directory"

	withPrepare := `{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "prepare": {"what": "build"}}}`

	results := Run(t.Context(), driverOptionsWith(t, "preparing", withPrepare))
	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}

	ran := false

	for _, r := range results {
		ran = ran || r.Check == name
	}

	if !ran {
		t.Errorf("no check %q", name)
	}

	for kind, fixture := range map[string]string{
		"idle":      withPrepare,
		"preparing": `{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}]}}`,
	} {
		failed := strings.Join(failedChecks(Run(t.Context(), driverOptionsWith(t, kind, fixture))), "\n")
		if !strings.Contains(failed, name) {
			t.Errorf("%s with %s passed: %s", kind, fixture, failed)
		}
	}
}

func TestTheKitChecksADriverThatValidates(t *testing.T) {
	const fixture = `{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "config": {"mode": "fast"}}}`

	results := Run(t.Context(), driverOptionsWith(t, "strict", fixture))
	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}

	var ran []string
	for _, r := range results {
		ran = append(ran, r.Check)
	}

	for _, want := range []string{
		"validate takes the fixture's settings",
		"validate refuses a key no driver takes, naming it",
		"each verb refuses what validate refuses, before it runs anything",
	} {
		if !strings.Contains(strings.Join(ran, "\n"), want) {
			t.Errorf("no check %q in %q", want, ran)
		}
	}

	for kind, want := range map[string]string{
		"lax":       "validate refuses a key no driver takes, naming it: run: []",
		"two-faced": "each verb refuses what validate refuses, before it runs anything: run: it took them and exited 0",
	} {
		failed := strings.Join(failedChecks(Run(t.Context(), driverOptionsWith(t, kind, fixture))), "\n")
		if !strings.Contains(failed, want) {
			t.Errorf("%s: failed:\n%s\nwant %q", kind, failed, want)
		}
	}

	for _, r := range Run(t.Context(), driverOptionsWith(t, "honest", fixture)) {
		if strings.Contains(r.Check, "validate") {
			t.Errorf("a driver that doesn't validate got %q", r.Check)
		}
	}
}

func TestADriverNotApartIsMarkedOnlyInAStackSuite(t *testing.T) {
	results := Run(t.Context(), driverOptions(t, "together"))

	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}

	var ran []string
	for _, r := range results {
		ran = append(ran, r.Check)
	}

	all := strings.Join(ran, "\n")
	if !strings.Contains(all, "a recorded stack suite's bundle marks each slice's start and stop") || strings.Contains(all, "a suite with correlators") {
		t.Fatalf("checks: %q", ran)
	}
}
