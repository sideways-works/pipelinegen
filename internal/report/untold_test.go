package report

import (
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

func TestSlicesNoCorrelatorCouldTellAreNamedAndNeverQuiet(t *testing.T) {
	sql := []recording.Mark{{Plugin: "sql", Reason: "no statement log"}}
	s := Suite{Name: "e2e", Slices: []Slice{
		{Name: "e2e/a.spec.ts", State: Passed, Recorded: true, CannotTell: sql},
		{Name: "e2e/b.spec.ts", State: Passed, Recorded: true},
	}}

	if state, desc := s.Status(); state != statusSuccess || !strings.Contains(desc, "1 could not be recorded") {
		t.Fatalf("one of two: %s %q", state, desc)
	}

	run := Run{Name: "pr", Commit: "0123456789ab", Suites: []Suite{s}}
	if _, desc := run.RunStatus(); !strings.Contains(desc, "1 could not be recorded") {
		t.Fatalf("run: %q", desc)
	}

	if body := Comment(run, 7, List); !strings.Contains(body, "`e2e e2e/a.spec.ts`: sql could not tell (no statement log)") {
		t.Fatalf("comment:\n%s", body)
	}

	s.Slices[1].CannotTell = sql
	if state, desc := s.Status(); state != statusError || !strings.Contains(desc, "no slice could be recorded: sql found no evidence of a recording in coverage/<slice>/sql") {
		t.Fatalf("every slice: %s %q", state, desc)
	}

	s.Slices = append(s.Slices, Slice{Name: "e2e/c.spec.ts", State: Running})
	if state, _ := s.Status(); state != statusPending {
		t.Fatalf("running: %s", state)
	}
}

func TestAStatusMasksWhatTestsAndPluginsWroteBeforeItCuts(t *testing.T) {
	const token = "plugin-leaked-0123456789abcdef"

	secret.Add(token)

	s := Suite{Name: "e2e", Slices: []Slice{
		{Name: "e2e/a.spec.ts", State: Passed, Recorded: true, CannotTell: []recording.Mark{{Plugin: "sql", Reason: "saw " + token}}},
	}}

	_, desc := s.Status()
	if strings.Contains(desc, token[:10]) || !strings.Contains(desc, secret.Mask) {
		t.Fatalf("status %q", desc)
	}

	if body := Comment(Run{Name: "pr", Suites: []Suite{s}}, 7, List); strings.Contains(body, token) {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestARunOfEverythingListsTheSlicesBoundToEverything(t *testing.T) {
	now := time.Date(2026, 3, 4, 3, 0, 0, 0, time.UTC)
	r := Run{Name: "nightly", Commit: "0123456789ab", All: true, Now: now,
		Suites: []Suite{{Name: "e2e", Slices: []Slice{{Name: "e2e/a.spec.ts", State: Passed}}}},
		Marked: map[string][]recording.Mark{
			"e2e/e2e/a.spec.ts": {{Plugin: "sql", Reason: "no statement log", Since: now.Add(-72 * time.Hour)}},
		}}

	body := Comment(r, 7, List)
	if !strings.Contains(body, "Bound to everything until a recording tells them (1 slice):") ||
		!strings.Contains(body, "- `e2e/e2e/a.spec.ts`: sql couldn't tell since 2026-03-01 (3 days): no statement log") {
		t.Fatalf("comment:\n%s", body)
	}

	r.All = false
	if body := Comment(r, 7, List); strings.Contains(body, "Bound to everything until") {
		t.Fatalf("change run:\n%s", body)
	}
}
