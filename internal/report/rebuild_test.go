package report

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func rebuildRun(check string) Run {
	return Run{
		Name: "nightly", Commit: strings.Repeat("c", 40),
		Suites: []Suite{{Name: "e2e", Slices: []Slice{{Label: "a.spec.ts", State: Passed}}}},
		Rebuild: &Rebuild{Check: check, Differences: []RebuildDifference{{
			Plugin: "uses", Call: "links", Inputs: []string{"texts/a.txt"},
			What: []string{`link {"from":"t:a","to":"t:b"}: fresh only`},
		}}},
	}
}

func TestARebuildThatDiffersFailsTheRun(t *testing.T) {
	r := rebuildRun(config.RebuildError)

	state, desc := r.RunStatus()
	if state != statusFailure || !strings.Contains(desc, "the cold rebuild differs from the cache in 1 answer (uses)") || len(desc) > maxDescription {
		t.Fatalf("%s: %s", state, desc)
	}

	body := render(r, List, false)
	if !strings.Contains(body, "The cold rebuild of the graph differs from the cache:") ||
		!strings.Contains(body, "uses links, file texts/a.txt: link") || strings.Contains(body, "Warnings:") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestARebuildUnderWarningOnlyWarns(t *testing.T) {
	r := rebuildRun(config.RebuildWarning)

	state, desc := r.RunStatus()
	if state != statusSuccess || !strings.Contains(desc, "1 warning") {
		t.Fatalf("%s: %s", state, desc)
	}

	if ws := r.Warnings(); len(ws) != 1 || !strings.Contains(ws[0], "uses links, file texts/a.txt") {
		t.Fatalf("warnings %v", ws)
	}
}

func TestARebuildThatCouldNotFinishIsAFault(t *testing.T) {
	r := rebuildRun(config.RebuildError)
	r.Rebuild.Differences = nil
	r.Rebuild.Error = "uses: links: exit 2"

	if state, desc := r.RunStatus(); state != statusFailure || !strings.HasPrefix(desc, "the cold rebuild of the graph could not finish") {
		t.Fatalf("%s: %s", state, desc)
	}

	r.Rebuild.Error = ""

	if state, desc := r.RunStatus(); state != statusSuccess || desc != "1 suite passed" {
		t.Fatalf("%s: %s", state, desc)
	}
}
