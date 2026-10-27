package kit

import (
	"strings"
	"testing"
)

func endJob(step string) string {
	return "jobs:\n- name: main-e2e\n  plan: []\n  ensure:\n" + step
}

func endTask(indent, script string) string {
	lines := []string{
		"task: end",
		"config:",
		"  platform: linux",
		"  run:",
		"    path: sh",
		"    args:",
		"    - -ec",
		"    - |",
	}

	for line := range strings.SplitSeq(script, "\n") {
		lines = append(lines, "      "+line)
	}

	return indent + strings.Join(lines, "\n"+indent) + "\n"
}

func TestAReportStepThatCannotRecordFailsTheJob(t *testing.T) {
	const passes = "set -eu\nrc=0\ntools/pipelinegen report suite --root trigger || rc=$?\n[ \"$rc\" = 4 ] && exit 4\nexit 0" //nolint:gosec // a script, no credential

	const swallows = "set -eu\ntools/pipelinegen report suite --root trigger || true"

	for name, c := range map[string]struct {
		pipeline, want string
	}{
		"in a try":  {endJob("    try:\n" + endTask("      ", passes)), "inside a try"},
		"swallowed": {endJob(endTask("    ", swallows)), "exits 0 when the tool exits 4"},
		"passed on": {endJob(endTask("    ", passes)), ""},
		"the tool itself": {endJob("    task: batch-end\n    config:\n      platform: linux\n      run:\n        path: tools/pipelinegen\n" +
			"        args: [report, batch-end, --batch, \"1\"]\n"), ""},
		"no report step": {endJob("    task: unlock\n    config:\n      platform: linux\n      run:\n        path: tools/pipelinegen\n" +
			"        args: [lock, release, x]\n    attempts: 2\n"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(swallowedUnrecorded(t.Context(), []byte(c.pipeline)), "; ")

			switch {
			case c.want == "" && got != "":
				t.Errorf("found %q", got)
			case c.want != "" && !strings.Contains(got, c.want):
				t.Errorf("found %q, want %q", got, c.want)
			}
		})
	}
}
