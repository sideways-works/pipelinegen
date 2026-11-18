package kit

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

var stepKinds = []string{"task", "get", "put", "set_pipeline"}

// hookKeys are a step's hooks. Concourse runs them after the step even when its time limit ended it,
// with a fresh context, so no limit around the step reaches them: each needs its own.
var hookKeys = []string{"ensure", "on_success", "on_failure", "on_error", "on_abort"}

// unlimitedSteps are the steps with no time limit of their own or around them: one that hangs holds its
// build, and its run's statuses pending, for ever.
func unlimitedSteps(text []byte) []string {
	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	jobs, _ := doc["jobs"].([]any) //nolint:errcheck // a pipeline without jobs has nothing to check

	var out []string

	for _, j := range jobs {
		job, ok := j.(map[string]any)
		if !ok {
			continue
		}

		name := fmt.Sprint(job["name"])

		// the job's own hooks run outside its plan
		out = append(out, unlimited(job["plan"], name, false)...)

		for _, h := range hookKeys {
			out = append(out, unlimited(job[h], name, false)...)
		}
	}

	return out
}

func unlimited(v any, job string, bounded bool) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, unlimited(e, job, bounded)...)
		}

		return out
	case map[string]any:
		var out []string

		inner := bounded || x["timeout"] != nil

		if kind := stepKind(x); kind != "" && !inner {
			out = append(out, fmt.Sprintf("job %s, %s %v: no time limit", job, kind, x[kind]))
		}

		for _, k := range []string{"do", "in_parallel", "steps", "try"} {
			if c, ok := x[k]; ok {
				out = append(out, unlimited(c, job, inner)...)
			}
		}

		for _, h := range hookKeys {
			if c, ok := x[h]; ok {
				out = append(out, unlimited(c, job, false)...)
			}
		}

		return out
	}

	return nil
}

func stepKind(x map[string]any) string {
	for _, k := range stepKinds {
		if _, ok := x[k]; ok {
			return k
		}
	}

	return ""
}
