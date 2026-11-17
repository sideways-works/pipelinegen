package kit

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/packages"
	"github.com/sideways-works/pipelinegen/plugin"
)

const packagesRun = "packages and cached images"

// packagesSpec is the spec of suites run by drivers that declare packages,
// one of them with a cached image on the planner's base, and planner
// packages of its own.
func packagesSpec() plugin.Spec {
	s := stepsSpec()

	planner := config.Packages{{Name: "curl"}}
	s.PlannerPackages = &plugin.SpecPackages{Names: packages.Names(planner), Install: packages.Script(planner)}
	s.Images = &plugin.SpecImages{Registry: "registry.example/ci-images", Credentials: "registry_push", PullCredentials: "registry_pull"}

	digest := ""
	if p, ok := s.CI["planner"].(map[string]any); ok {
		digest = fmt.Sprint(p["digest"])
	}

	for i := range s.Suites {
		ps := config.Packages{{Name: "jq"}, {Name: "git"}}
		sp := &plugin.SpecPackages{Names: packages.Names(ps), Install: packages.Script(ps)}

		if i == 0 {
			key := packages.Key(digest, ps)
			sp.Images = map[string]string{s.Suites[i].Image: key}
			s.Images.Builds = append(s.Images.Builds, plugin.SpecImageBuild{
				Key: key, Base: s.Suites[i].Image, Digest: digest, Names: sp.Names, Install: sp.Install,
			})
		}

		s.Suites[i].Packages = sp
	}

	return s
}

// packageProblems are, in a pipeline written for packagesSpec, the steps
// the runner wrote that don't install their packages first, the cached
// images it never names, and the registry's credentials where they don't
// belong.
func packageProblems(s plugin.Spec, steps []stepCall, text []byte) []string {
	var out []string

	installs := map[string]string{}

	for _, su := range s.Suites {
		if su.Packages != nil {
			installs[su.Name] = su.Packages.Install
		}
	}

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") {
			continue
		}

		want, of := s.PlannerPackages.Install, "concourse.planner.packages"
		if len(sc.args) > 1 && sc.args[0] == sliceCommand && (sc.args[1] == "run" || sc.args[1] == "prepare") {
			want, of = installs[sc.env["PIPELINEGEN_SUITE"]], "the suite's packages"
		}

		if want != "" && !strings.Contains(sc.script, want) {
			out = appendNew(out, fmt.Sprintf("%s: pipelinegen %s runs without installing %s first", sc.where, strings.Join(sc.args, " "), of))
		}
	}

	for _, b := range s.Images.Builds {
		if !strings.Contains(string(text), b.Key) {
			out = appendNew(out, "the cached image "+packages.Short(b.Key)+" on "+b.Base+" is never built or used")
		}
	}

	return append(out, credentialProblems(s, text)...)
}

// credentialProblems are the steps that hold the registry's credentials
// and run the repository's code: a task file, or a task given its source.
// The pushing var reaches no job that does, the pulling var no such task.
func credentialProblems(s plugin.Spec, text []byte) []string {
	var doc struct {
		Jobs []map[string]any `yaml:"jobs"`
	}

	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	push, pull := varsOf(s.Images.Credentials), varsOf(s.Images.PullCredentials)

	var out []string

	for _, job := range doc.Jobs {
		name := fmt.Sprint(job["name"])

		var runsCode, pushes bool

		walkSteps(job, func(step map[string]any) {
			code := codeTask(step)
			runsCode = runsCode || code

			data, err := yaml.Marshal(step)
			if err != nil {
				return
			}

			holds := func(vars []string) bool {
				return slices.ContainsFunc(vars, func(v string) bool { return strings.Contains(string(data), "(("+v+"))") })
			}

			pushes = pushes || holds(push)

			if code && (holds(pull) || holds(push)) {
				out = appendNew(out, fmt.Sprintf("job %s, task %v: holds the registry's credentials and runs the repository's code", name, step["task"]))
			}
		})

		if runsCode && pushes {
			out = appendNew(out, "job "+name+" holds the var that pushes cached images and runs the repository's code")
		}
	}

	return out
}

// varsOf are the references a var's fields are given by.
func varsOf(v string) []string {
	if v == "" {
		return nil
	}

	return []string{v, v + ".username", v + ".password"}
}

// codeTask tells whether a task step runs the repository's code: a task
// file of it, or a config given its checkout.
func codeTask(step map[string]any) bool {
	if step["task"] == nil {
		return false
	}

	if _, ok := step["file"].(string); ok {
		return true
	}

	cfg, _ := step["config"].(map[string]any) //nolint:errcheck // a task without a config runs nothing of the repository's

	return slices.Contains(names(cfg["inputs"], "name"), "source")
}

// walkSteps calls f with every step of a job, hooks and nested steps
// included, the job's own map first.
func walkSteps(v any, f func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		f(x)

		for _, k := range slices.Sorted(maps.Keys(x)) {
			walkSteps(x[k], f)
		}
	case []any:
		for _, e := range x {
			walkSteps(e, f)
		}
	}
}

// installsOf are the spec's install scripts.
func installsOf(s plugin.Spec) []string {
	var out []string

	if s.PlannerPackages != nil {
		out = append(out, s.PlannerPackages.Install)
	}

	for _, su := range s.Suites {
		if su.Packages != nil {
			out = append(out, su.Packages.Install)
		}
	}

	return out
}
