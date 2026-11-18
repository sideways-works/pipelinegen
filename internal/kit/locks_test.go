package kit

import (
	"strings"
	"testing"
)

func TestALockStepIsRefused(t *testing.T) {
	for text, want := range map[string]string{
		"jobs:\n- {name: e2e, plan: [{task: lock, config: {run: {path: tools/pipelinegen, args: [lock, acquire, browsers, --root, source]}}}]}\n":                     "job e2e, step lock: runs pipelinegen lock",
		"jobs:\n- {name: e2e, plan: [{task: start, config: {run: {path: sh, args: [-ec, 'tools/pipelinegen lock acquire \"$PIPELINEGEN_LOCK\" --root source']}}}]}\n": "job e2e, step start: runs pipelinegen lock",
		"jobs:\n- {name: e2e, ensure: {task: end, config: {run: {path: sh, args: [-ec, 'tools/pipelinegen unlock browsers']}}}, plan: []}\n":                          "job e2e, step end: runs pipelinegen unlock",
		"jobs:\n- {name: e2e, plan: [{task: start, config: {run: {path: sh, args: [-ec, 'echo lock; tools/pipelinegen tree --out source']}}}]}\n":                     "",
	} {
		got := strings.Join(lockSteps([]byte(text), nil), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want %q", text, got, want)
		}
	}
}
