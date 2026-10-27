package kit

import (
	"strings"
	"testing"
)

func TestEveryStepNeedsATimeLimit(t *testing.T) {
	for want, text := range map[string]string{
		"": `jobs:
- name: a
  plan:
  - in_parallel: [{get: trigger}]
    timeout: 20m
  - task: start
    timeout: 20m
    config: {}
  - across: [{var: batch, values: [1]}]
    do:
    - task: batch
      timeout: 120m
      file: x.yml
      ensure: {task: batch-end, timeout: 20m, config: {}}
  ensure: {try: {task: end, timeout: 20m, config: {}}}
`,
		"job a, get trigger: no time limit": `jobs:
- name: a
  plan:
  - in_parallel: [{get: trigger}]
`,
		"job a, task batch-end: no time limit": `jobs:
- name: a
  plan:
  - do:
    - task: batch
      file: x.yml
      ensure: {task: batch-end, config: {}}
    timeout: 60m
`,
		"job a, task end: no time limit": `jobs:
- name: a
  plan:
  - {task: start, timeout: 5m, config: {}}
  ensure: {try: {task: end, config: {}}}
`,
		"job a, put planned: no time limit": `jobs:
- name: a
  plan:
  - in_parallel: {steps: [{put: planned}, {get: x, timeout: 1m}]}
`,
	} {
		got := strings.Join(unlimitedSteps([]byte(text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func TestALocksWaitIsNoPartOfTheBatchsLimit(t *testing.T) {
	lock := `{task: lock, config: {run: {path: tools/pipelinegen, args: [lock, acquire, browsers, --root, source]}}}`

	bad := "jobs:\n- name: a\n  plan:\n  - do:\n    - " + lock + "\n    - {task: batch, file: x.yml}\n    timeout: 60m\n"
	if got := strings.Join(waitBoundWithBatch([]byte(bad)), "; "); !strings.Contains(got, "the wait for lock browsers's slot counts against the time limit") {
		t.Errorf("a wait under the batch's limit: %q", got)
	}

	good := "jobs:\n- name: a\n  plan:\n  - do:\n    - " + strings.Replace(lock, "{task: lock,", "{task: lock, timeout: 260m,", 1) +
		"\n    - {task: batch, file: x.yml, timeout: 60m}\n"
	if got := waitBoundWithBatch([]byte(good)); len(got) != 0 {
		t.Errorf("a wait beside the batch: %q", got)
	}
}
