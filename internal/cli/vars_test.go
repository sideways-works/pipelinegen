package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Concourse refuses a build whose plan, with the vars filled in, doesn't decode into these types.
type concourseTask struct {
	Run struct {
		Path string   `json:"path"`
		Args []string `json:"args"`
	} `json:"run"`
	Params map[string]string `json:"params"`
}

// A field that is exactly "((.:var))" becomes the value's JSON, as Concourse fills it.
func concourseTakes(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("vars: %w", err)
	}

	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("%s: across takes a list: %w", path, err)
	}

	const placeholder = `"((.:v))"`

	template := []byte(`{"run": {"path": "pipelinegen", "args": ["report", "batch-end", "--batch", ` + placeholder +
		`]}, "params": {"PIPELINEGEN_BATCH": ` + placeholder + `}}`)

	for _, v := range values {
		filled := bytes.ReplaceAll(template, []byte(placeholder), v)

		var task concourseTask
		if err := json.Unmarshal(filled, &task); err != nil {
			return fmt.Errorf("%s: the value %s in a task config: %w", path, v, err)
		}
	}

	return nil
}

func TestPipelineVarsListsWhatSettingNeeds(t *testing.T) {
	text := []byte(`resources:
- source: {password: ((github_token)), uri: x}
jobs:
- plan:
  - across: [{var: batch, values: ((.:batches))}]
  - task: prepare
    params: {NPM_TOKEN: ((npm_token)), B: ((.:batch)), C: ((github_token)), D: ((vault:deploy_key))}
`)

	if got := strings.Join(pipelineVars(text), ","); got != "github_token,npm_token,vault:deploy_key" {
		t.Fatalf("vars %q", got)
	}
}
