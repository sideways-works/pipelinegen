package kit

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

// envSerialPlan is what runners once gave a serial planning job's plan
// step; the tool reads it no more, and a pipeline that still sets it was
// written for a serial planning job.
const envSerialPlan = "PIPELINEGEN_SERIAL_PLAN"

// serializedJobs are the jobs that run their builds one at a time, or behind
// other jobs, though the spec asks for none of it: only a suite job whose
// suite names a serial group or max_in_flight may. A planning job never is.
func serializedJobs(s plugin.Spec, text []byte) []string {
	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	asked, planning := jobsAsked(s)

	jobs, _ := doc["jobs"].([]any) //nolint:errcheck // a pipeline without jobs has none

	var out []string

	for _, j := range jobs {
		job, ok := j.(map[string]any)
		if !ok {
			continue
		}

		name := fmt.Sprint(job["name"])

		for _, key := range []string{"serial", "serial_groups", "max_in_flight"} {
			if _, ok := job[key]; !ok || asked[name] {
				continue
			}

			if planning[name] {
				out = append(out, fmt.Sprintf("job %s: a continuous run's planning job with %s: its builds would queue, though each decides beside the others from the run's records", name, key))

				continue
			}

			out = append(out, fmt.Sprintf("job %s: %s, which nothing asks for", name, key))
		}

		if saysSerialPlan(job) {
			out = append(out, "job "+name+": "+envSerialPlan+", which the tool no longer reads: the job was written for a serial planning job")
		}
	}

	slices.Sort(out)

	return out
}

// planning names a continuous run's planning job.
func jobsAsked(s plugin.Spec) (asked, planning map[string]bool) {
	asked, planning = map[string]bool{}, map[string]bool{}

	for _, r := range s.Runs {
		planning["plan-"+r.Name] = r.Trigger.Kind == plugin.TriggerContinuous

		for _, rs := range r.Suites {
			i := slices.IndexFunc(s.Suites, func(su plugin.SpecSuite) bool { return su.Name == rs.Suite })
			if i < 0 {
				continue
			}

			job := r.Name + "-" + rs.Suite
			asked[job] = s.Suites[i].SerialGroup != "" || s.Suites[i].MaxInFlight > 0
		}
	}

	return asked, planning
}

// walkJobs visits every mapping under v, with the job it is in.
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

func saysSerialPlan(job map[string]any) bool {
	found := false

	walkJobs(job, "", func(x map[string]any, _ string) {
		if _, ok := x[envSerialPlan]; ok {
			found = true
		}
	})

	return found
}

// unplannedStarts are a continuous run's suite jobs that could start a build
// for a commit its planning job didn't plan: a suite job's triggering get
// must take what the planning job puts inside an across, never the branch.
func unplannedStarts(s plugin.Spec, text []byte) []string {
	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	jobs := map[string]map[string]any{}

	list, _ := doc["jobs"].([]any) //nolint:errcheck // a pipeline without jobs has none
	for _, j := range list {
		if job, ok := j.(map[string]any); ok {
			jobs[fmt.Sprint(job["name"])] = job
		}
	}

	var out []string

	for _, r := range s.Runs {
		if r.Trigger.Kind != plugin.TriggerContinuous {
			continue
		}

		puts := gatedPuts(jobs["plan-"+r.Name])

		for _, rs := range r.Suites {
			name := r.Name + "-" + rs.Suite

			for _, res := range triggeringResources(jobs[name]) {
				if !puts[res] {
					out = append(out, "job "+name+": triggers on "+res+", which its planning job doesn't put only for a commit it planned: an exited build would start it")
				}
			}
		}
	}

	slices.Sort(out)

	return out
}

// unhandedStarts are a pull request run's suite jobs that could start a
// build for a head no build handed on: each resource a suite job triggers on
// must be put only inside an across, and plan --from must write the gate.
func unhandedStarts(s plugin.Spec, text []byte) []string {
	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	gated, loose := map[string]bool{}, map[string][]string{}

	jobs := map[string]map[string]any{}

	list, _ := doc["jobs"].([]any) //nolint:errcheck // a pipeline without jobs has none
	for _, j := range list {
		job, ok := j.(map[string]any)
		if !ok {
			continue
		}

		name := fmt.Sprint(job["name"])
		jobs[name] = job

		maps.Copy(gated, gatedPuts(job))

		for res := range ungatedPuts(job) {
			loose[res] = append(loose[res], name)
		}
	}

	var out []string

	for _, r := range s.Runs {
		if r.Trigger.Kind != plugin.TriggerPullRequests {
			continue
		}

		for _, rs := range r.Suites {
			name := r.Name + "-" + rs.Suite

			for _, res := range triggeringResources(jobs[name]) {
				switch {
				case len(loose[res]) > 0:
					out = append(out, "job "+name+": triggers on "+res+", which job "+strings.Join(loose[res], ", job ")+" puts outside an across: a planning build of a head planned already would start it")
				case !gated[res]:
					out = append(out, "job "+name+": triggers on "+res+", which no job puts inside an across: it starts for whatever its planning didn't hand on")
				}
			}
		}
	}

	slices.Sort(out)

	steps, err := stepCalls(text)
	if err != nil {
		return append(out, err.Error())
	}

	for _, sc := range steps {
		if len(sc.args) > 0 && sc.args[0] == planCmd && flagValue(sc.args, "from") != "" && flagValue(sc.args, "gate") == "" {
			out = appendNew(out, sc.where+": plan --from without --gate: the job can't tell a plan it wrote from a head planned already")
		}
	}

	return out
}

func ungatedPuts(job map[string]any) map[string]bool {
	puts := map[string]bool{}

	var walk func(v any)

	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["across"]; ok {
				return
			}

			if p, ok := x["put"].(string); ok {
				puts[p] = true
			}

			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}

	walk(job)

	return puts
}

func gatedPuts(job map[string]any) map[string]bool {
	puts := map[string]bool{}

	walkJobs(job, "", func(x map[string]any, _ string) {
		if _, ok := x["across"]; !ok {
			return
		}

		walkJobs(x["do"], "", func(y map[string]any, _ string) {
			if p, ok := y["put"].(string); ok {
				puts[p] = true
			}
		})
	})

	return puts
}

func triggeringResources(job map[string]any) []string {
	var out []string

	walkJobs(job, "", func(x map[string]any, _ string) {
		get, ok := x["get"].(string)
		if !ok || x["trigger"] != true {
			return
		}

		if res, ok := x["resource"].(string); ok {
			get = res
		}

		out = append(out, get)
	})

	return out
}

// undecidedPrepares are a continuous run's planning tasks that could run the
// planner's prepare before the build knows it will plan: the task's first
// plan call must be the decision (plan --decide --gate <dir>), with the plan
// after it through the same directory.
func undecidedPrepares(s plugin.Spec, text []byte) []string {
	steps, err := stepCalls(text)
	if err != nil {
		return []string{err.Error()}
	}

	var out []string

	for _, r := range s.Runs {
		if r.Trigger.Kind != plugin.TriggerContinuous {
			continue
		}

		job := "job plan-" + r.Name + ","

		var plans []stepCall

		for _, sc := range steps {
			if strings.HasPrefix(sc.where, job) && len(sc.args) > 0 && sc.args[0] == "plan" {
				plans = append(plans, sc)
			}
		}

		if problem := decidedFirst(plans); problem != "" {
			out = append(out, "job plan-"+r.Name+": "+problem)
		}
	}

	return out
}

func decidedFirst(plans []stepCall) string {
	if len(plans) < 2 || !slices.Contains(plans[0].args, "--decide") {
		return "its planning task doesn't ask for the decision (plan --decide) before the prepare and the plan"
	}

	gate := flagValue(plans[0].args, "gate")
	if gate == "" || slices.Contains(plans[1].args, "--decide") || flagValue(plans[1].args, "gate") != gate || plans[0].where != plans[1].where {
		return "its decision and its plan don't share one task and one --gate directory"
	}

	return ""
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		for _, dash := range []string{"-", "--"} {
			if a == dash+name && i+1 < len(args) {
				return args[i+1]
			}

			if v, ok := strings.CutPrefix(a, dash+name+"="); ok {
				return v
			}
		}
	}

	return ""
}
