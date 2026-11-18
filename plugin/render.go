package plugin

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/plugkit/semver"
)

// CallRender asks a driver for a suite's run step in a CI system's own steps, so that system runs
// the test tool itself instead of pipelinegen running the driver in a container. It is no verb of
// a step: a runner calls it while it writes the pipeline, for the targets the driver's Renders name.
const CallRender = "render"

// Render is one CI system a driver renders steps for: Versions is a semver range of the target's
// versions, Plugins the target's own plugins its steps use, each with the range of their versions.
type Render struct {
	Target   string            `json:"target"`
	Versions string            `json:"versions"`
	Plugins  map[string]string `json:"plugins,omitempty"`
}

// The shapes of the work a rendered step takes: a pool's slices are known only when the target
// expands it and come in chunks, a batch's are a fixed list, for a target without pools.
const (
	ShapePool  = "pool"
	ShapeBatch = "batch"
)

type RenderShape struct {
	Kind string `json:"kind"`
}

// RenderStep is the suite's run step, as its config writes it.
type RenderStep struct {
	Run  string         `json:"run"`
	With map[string]any `json:"with,omitempty"`
}

// RenderSetup is one of the suite's set-ups, as its config writes it.
type RenderSetup struct {
	Setup string         `json:"setup"`
	With  map[string]any `json:"with,omitempty"`
}

// RenderRequest asks for one suite's run step. Secrets are the names of the suite's batch secrets,
// never their values: a pipeline is written long before any value exists, and a rendered step
// names each secret for the target to fill in.
type RenderRequest struct {
	Request

	Target        string            `json:"target"`
	TargetVersion string            `json:"target_version"`
	Suite         string            `json:"suite"`
	Step          RenderStep        `json:"step"`
	Setup         []RenderSetup     `json:"setup,omitempty"`
	Shape         RenderShape       `json:"shape"`
	Record        bool              `json:"record"`
	ObservedBy    []string          `json:"observed_by,omitempty"`
	Image         string            `json:"image,omitempty"`
	Packages      []string          `json:"packages,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	Secrets       []string          `json:"secrets,omitempty"`
}

// How a rendered step reports where each slice starts and ends: through the target's plugin
// protocol, or in a marks file the target reads after the step.
const (
	MarksProtocol = "protocol"
	MarksFile     = "file"
)

// RenderAnswer is the target's own steps, as JSON objects of the target's schema, and what they
// declare: how they mark slices, the directory of their per-slice results (relative to the step's
// working directory, one result file per slice in a batch's result format) and the caches they
// mount, by name, at absolute paths.
type RenderAnswer struct {
	Steps   []json.RawMessage `json:"steps"`
	Marks   string            `json:"marks"`
	Results string            `json:"results"`
	Caches  map[string]string `json:"caches,omitempty"`
}

// RendersProblem says what is wrong with a describe's renders, empty when nothing is.
func RendersProblem(renders []Render) string {
	var problems []string

	seen := map[string]bool{}

	for _, r := range renders {
		switch {
		case r.Target == "":
			problems = append(problems, "a render names no target")

			continue
		case seen[r.Target]:
			problems = append(problems, fmt.Sprintf("target %s is rendered twice", r.Target))
		}

		seen[r.Target] = true

		if _, err := semver.ParseRange(r.Versions); err != nil {
			problems = append(problems, fmt.Sprintf("target %s: versions %q: %v", r.Target, r.Versions, err))
		}

		for _, name := range slices.Sorted(maps.Keys(r.Plugins)) {
			if _, err := semver.ParseRange(r.Plugins[name]); err != nil || name == "" {
				problems = append(problems, fmt.Sprintf("target %s: plugin %q: range %q: %v", r.Target, name, r.Plugins[name], err))
			}
		}
	}

	return strings.Join(problems, "; ")
}

// Problem says what is wrong with an answer, empty when nothing is.
func (a RenderAnswer) Problem() string {
	var problems []string

	if len(a.Steps) == 0 {
		problems = append(problems, "no steps")
	}

	for i, s := range a.Steps {
		var obj map[string]any
		if json.Unmarshal(s, &obj) != nil || obj == nil {
			problems = append(problems, fmt.Sprintf("step %d is no JSON object", i+1))
		}
	}

	if a.Marks != MarksProtocol && a.Marks != MarksFile {
		problems = append(problems, fmt.Sprintf("marks %q is neither %s nor %s", a.Marks, MarksProtocol, MarksFile))
	}

	if r := a.Results; r == "" || path.IsAbs(r) || path.Clean(r) == ".." || strings.HasPrefix(path.Clean(r), "../") {
		problems = append(problems, fmt.Sprintf("results %q is no directory inside the step's", r))
	}

	for _, name := range slices.Sorted(maps.Keys(a.Caches)) {
		if name == "" || !path.IsAbs(a.Caches[name]) {
			problems = append(problems, fmt.Sprintf("cache %q is mounted at %q, not an absolute path", name, a.Caches[name]))
		}
	}

	return strings.Join(problems, "; ")
}

// RenderTarget is a runner's side of a native render: the CI system and version it writes for,
// the driver versions it renders natively (Accepts, by driver name; one left out, any), and the
// version the target's config pins each of the target's plugins at.
type RenderTarget struct {
	Target  string
	Version string
	Accepts map[string]string
	Pinned  map[string]string
}

// RenderChoice is native with the driver's Render, or the fallback with a Notice for the
// pipeline's notices, naming the first condition that failed.
type RenderChoice struct {
	Native bool
	Render Render
	Notice string
}

// Choose says whether the runner renders the driver's steps natively: when the driver's range for
// the target admits the target's version, the runner's range for the driver admits the driver's
// version, and every plugin the render uses is pinned at a version its range admits. It never
// fails: whatever doesn't hold, a malformed range or version included, falls back.
func (t RenderTarget) Choose(d Description) RenderChoice {
	driver := d.Name + " " + d.Version

	fallback := func(format string, args ...any) RenderChoice {
		return RenderChoice{Notice: fmt.Sprintf(format, args...) + ": fallback"}
	}

	i := slices.IndexFunc(d.Renders, func(r Render) bool { return r.Target == t.Target })
	if i < 0 {
		return fallback("%s renders no %s steps", driver, t.Target)
	}

	r := d.Renders[i]

	want, err := semver.Parse(t.Version)
	if err != nil {
		return fallback("this runner's %s version %q is no version", t.Target, t.Version)
	}

	versions, err := semver.ParseRange(r.Versions)
	if err != nil {
		return fallback("%s renders %s %q, which is no range", driver, t.Target, r.Versions)
	}

	if !versions.Contains(want) {
		return fallback("%s renders %s %s; this runner targets %s", driver, t.Target, r.Versions, t.Version)
	}

	if accepts, ok := t.Accepts[d.Name]; ok {
		rng, err := semver.ParseRange(accepts)
		if err != nil {
			return fallback("this runner renders %s %q natively, which is no range", d.Name, accepts)
		}

		v, err := semver.Parse(d.Version)
		if err != nil || !rng.Contains(v) {
			return fallback("this runner renders %s %s natively; this is %s", d.Name, accepts, d.Version)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(r.Plugins)) {
		pinned, ok := t.Pinned[name]
		if !ok {
			return fallback("%s renders %s steps with the plugin %s, which %s doesn't pin", driver, t.Target, name, t.Target)
		}

		rng, err := semver.ParseRange(r.Plugins[name])
		if err != nil {
			return fallback("%s renders with %s %q, which is no range", driver, name, r.Plugins[name])
		}

		v, err := semver.Parse(pinned)
		if err != nil || !rng.Contains(v) {
			return fallback("%s renders with %s %s; %s pins %s", driver, name, r.Plugins[name], t.Target, pinned)
		}
	}

	return RenderChoice{Native: true, Render: r}
}
