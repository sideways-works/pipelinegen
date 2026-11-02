package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

const pluginPlan = `{"narrowing": 3, "suites": [{"suite": "e2e",
  "selected": [{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts"}],
  "reasons": {"@pipelinegen_deps//slices:e2e/a.spec.ts": [{"kind": "executed", "what": "go:notes#func:Create"}]},
  "narrowed": [{"kind": "sampled", "narrowing": "sample", "off_at": 4, "reason": "setup-only code changed", "reached": 2, "dropped": ["e2e/b.spec.ts"]}]}],
 "lists": {"commit": "0123456789abcdef0123456789abcdef01234567", "back": 0,
  "cannot_tell": {"e2e/e2e/a.spec.ts": [{"plugin": "sql", "reason": "no statement log", "since": "2026-11-10T03:00:00Z"}]}},
 "plugins": {"calls": [{"plugin": "go", "call": "units", "asked": 3, "cached": 120}], "warnings": ["plugin ts could not tell: toolchain"]}}`

func TestWhyNamesTheNarrowingThePluginsAndWhoCouldntTell(t *testing.T) {
	root, _, store := runEnv(t)
	commit := "0123456789abcdef0123456789abcdef01234567"

	if err := report.WritePlan(t.Context(), store, report.ID{Run: "pr", Change: "pr-7", Commit: commit}, []byte(pluginPlan)); err != nil {
		t.Fatal(err)
	}

	for slice, want := range map[string][]string{
		"e2e/b.spec.ts": {
			"e2e e2e/b.spec.ts was left out in pr pr-7 at 0123456789ab:",
			"not in the sample: setup-only code changed; the nightly runs it; it would have run at narrowing level 4 or above (the plan used 3)",
		},
		"e2e/a.spec.ts": {
			"e2e e2e/a.spec.ts was selected in pr pr-7 at 0123456789ab:",
			"sql could not tell what it touched when it last recorded (no statement log), first on 2026-11-10; it is bound to everything until a recording tells again, and its list is the one from before",
		},
	} {
		stdout, stderr, code := run(t, "why", "--root", root, "--run", "pr", "--change", "pr-7", "--slice", slice, "--verbose")
		if code != 0 {
			t.Fatalf("%s: %d %s", slice, code, stderr)
		}

		for _, line := range append(want,
			"plugins: 120 of 123 files answered from the cache",
			"  go units: 3 asked, 120 from the cache",
			"warning: plugin ts could not tell: toolchain") {
			if !strings.Contains(stdout, line+"\n") {
				t.Errorf("%s: no line %q in\n%s", slice, line, stdout)
			}
		}
	}
}
