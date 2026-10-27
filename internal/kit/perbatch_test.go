package kit

import (
	"strings"
	"testing"
)

func perBatchTask(name, args, params string) string {
	return `
        - task: ` + name + `
          config:
            platform: linux
            run: {path: tools/pipelinegen, args: [` + args + `]}
            params: {` + params + `}`
}

func TestPerBatchLocks(t *testing.T) {
	good := `jobs:
- name: full-e2e
  plan:
  - across: [{var: batch, values: [1, 2]}]
    do:
      - do:` + perBatchTask("lock", "lock, acquire, browsers", "PIPELINEGEN_BATCH: ((.:batch))") + perBatchTask("batch", "slice, list", "") + `
        ensure:` + perBatchTask("unlock", "lock, release, browsers", "PIPELINEGEN_BATCH: ((.:batch))") + `
`
	if got := perBatchProblems([]byte(good)); len(got) != 0 {
		t.Fatalf("per batch: %v", got)
	}

	whole := `jobs:
- name: main-e2e
  plan:
  - do:` + perBatchTask("start", "lock, acquire, browsers", "") + `
    ensure:` + perBatchTask("end", "lock, release, browsers", "") + `
`
	if got := perBatchProblems([]byte(whole)); len(got) != 1 || !strings.Contains(got[0], "for the whole build") {
		t.Fatalf("for the whole build: %v", got)
	}

	unfreed := `jobs:
- name: main-e2e
  plan:` + strings.ReplaceAll(perBatchTask("lock", "lock, acquire, browsers", "PIPELINEGEN_BATCH: ((.:batch))"), "\n      ", "\n  ") + `
`
	if got := perBatchProblems([]byte(unfreed)); len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "outside a step that frees it") {
		t.Fatalf("freed nowhere: %v", got)
	}
}
