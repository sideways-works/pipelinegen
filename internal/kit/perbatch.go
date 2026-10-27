package kit

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// perBatchProblems are, with locks taken per batch, the acquires that aren't
// taken around one batch or whole-suite check and freed in that step's
// ensure, and the suite jobs that take a lock but never per batch.
func perBatchProblems(text []byte) []string {
	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	guarded, perBatch := map[string]bool{}, map[string]bool{}

	walkJobs(doc, "", func(x map[string]any, job string) {
		do, ok := x["do"].([]any)
		if !ok || x["ensure"] == nil {
			return
		}

		released := lockCalls(x["ensure"], "release", job)

		for _, c := range lockCalls(do, "acquire", job) {
			if slices.ContainsFunc(released, func(r lockCall) bool { return r.lock == c.lock }) {
				guarded[c.key] = true
			}

			if _, ok := c.env["PIPELINEGEN_BATCH"]; ok {
				perBatch[job] = true
			}
		}
	})

	var out []string

	jobs := map[string]bool{}

	for _, c := range lockCalls(doc, "acquire", "") {
		jobs[c.job] = true

		if !guarded[c.key] {
			out = appendNew(out, c.where+": takes lock "+c.lock+" outside a step that frees it in its ensure")
		}
	}

	for job := range jobs {
		if !perBatch[job] {
			out = appendNew(out, "job "+job+": takes its lock for the whole build, not per batch")
		}
	}

	slices.Sort(out)

	return out
}

func walkJobs(v any, job string, visit func(x map[string]any, job string)) {
	switch x := v.(type) {
	case map[string]any:
		if name, ok := x["name"].(string); ok && job == "" {
			if _, isJob := x["plan"]; isJob {
				job = name
			}
		}

		visit(x, job)

		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}

		slices.Sort(keys)

		for _, k := range keys {
			walkJobs(x[k], job, visit)
		}
	case []any:
		for _, e := range x {
			walkJobs(e, job, visit)
		}
	}
}

type lockCall struct {
	key, where, job, lock string
	env                   map[string]string
}

func lockCalls(v any, kind, job string) []lockCall {
	var out []lockCall

	walkJobs(v, job, func(x map[string]any, job string) {
		if cfg, ok := x["config"].(map[string]any); ok && x["task"] != nil {
			out = append(out, taskLockCalls(x, cfg, kind, job)...)
		}
	})

	return out
}

func taskLockCalls(step, cfg map[string]any, kind, job string) []lockCall {
	var out []lockCall

	where := fmt.Sprintf("job %s, step %v", job, step["task"])

	for i, c := range taskCalls(where, step, cfg) {
		if len(c.args) < 3 || c.args[0] != "lock" || c.args[1] != kind {
			continue
		}

		name := c.args[2]

		// a lock a script takes only when a variable names it
		if v, ok := strings.CutPrefix(strings.Trim(name, `"{}`), "$"); ok {
			if c.env[v] == "" {
				continue
			}

			name = c.env[v]
		}

		out = append(out, lockCall{key: fmt.Sprintf("%s#%d", where, i), where: where, job: job, lock: name, env: c.env})
	}

	return out
}
