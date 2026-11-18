package kit

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

const placementCheck = "a placement moves steps between workers and changes nothing else"

// staleHashParam holds a pipeline's hash of itself, which any change to
// it changes, a worker tag included.
const staleHashParam = "PIPELINEGEN_PIPELINE_SHA256"

// placement checks that the runner reads its placement from its own config and that one only moves
// steps between workers: the pipeline with it is the one without it but for worker tags and the
// gets a moved step needs fetched on its worker (movedGets).
func (k *kit) placement(ctx context.Context, root string, placed []byte) {
	const name = placementCheck

	k.Check("describe says it reads its placement from its own config", ifNot(k.can(plugin.CapPlacement),
		"no "+plugin.CapPlacement+" capability: pipelinegen refuses a runner without it"))

	if _, ok := k.o.Config[config.PlacementKey]; !ok {
		k.Skip(name, "the config the kit was given has no "+config.PlacementKey)

		return
	}

	bare := k.p
	bare.Config = maps.Clone(k.o.Config)
	delete(bare.Config, config.PlacementKey)

	work := k.WorkDir()

	var ans plugin.PipelineAnswer
	if err := k.call(ctx, plugin.CallPipeline, plugin.PipelineRequest{Request: bare.NewRequest(work), Spec: k.pinned(Spec()), Root: root}, &ans); err != nil || len(ans.Files) != 1 {
		k.Check(name, "the pipeline without the placement: %v (%d files)", err, len(ans.Files))

		return
	}

	unplaced, err := os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))) //nolint:gosec // inside the call's work directory
	if err != nil {
		k.Check(name, "%v", err)

		return
	}

	a, errA := parsed(placed)
	b, errB := parsed(unplaced)

	if errA != nil || errB != nil {
		k.Check(name, "a pipeline isn't YAML: %v %v", errA, errB)

		return
	}

	a, b = strip(a), strip(b)
	problems := movedGets(a, b)
	idle(a)
	idle(b)

	switch {
	case len(problems) > 0 || canonical(a) != canonical(b):
		problems = append(problems, differences("", b, a)...)
		if len(problems) == 0 {
			problems = []string{"in what the kit can't place"}
		}

		k.Check(name, "the pipelines with and without the placement differ in more than worker tags: %s", strings.Join(firstFew(problems), "; "))
	case canonicalYAML(placed) == canonicalYAML(unplaced):
		k.Check(name, "the placement moved no step: give the kit a placement that places one")
	default:
		k.Check(name, "")
	}
}

func parsed(text []byte) (any, error) {
	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, err //nolint:wrapcheck // said with the check's name
	}

	return doc, nil
}

func canonicalYAML(text []byte) string {
	var doc any
	if yaml.Unmarshal(text, &doc) != nil {
		return string(text)
	}

	return canonical(doc)
}

func strip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}

		for key, val := range x {
			switch key {
			case "tags":
			case staleHashParam:
				out[key] = ""
			default:
				out[key] = strip(val)
			}
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = strip(e)
		}

		return out
	}

	return v
}

// movedGets takes out of the placed pipeline the gets a placement may add, so that a step it moves
// has its inputs and image fetched on its worker: each fetches a resource the pipeline declares as
// a get its job has without the placement does, under a name of its own and never triggering. A
// task's image or input_mapping naming one is made to name the get it copies. What it can't take
// out it says.
func movedGets(placed, unplaced any) []string {
	declared := map[string]bool{}

	for _, r := range listAt(unplaced, "resources") {
		if m, ok := r.(map[string]any); ok {
			declared[fmt.Sprint(m["name"])] = true
		}
	}

	before := map[string]any{}

	for _, j := range listAt(unplaced, "jobs") {
		if m, ok := j.(map[string]any); ok {
			before[fmt.Sprint(m["name"])] = m
		}
	}

	var problems []string

	for _, j := range listAt(placed, "jobs") {
		if job, ok := j.(map[string]any); ok && before[fmt.Sprint(job["name"])] != nil {
			problems = append(problems, jobGets(job, before[fmt.Sprint(job["name"])], declared)...)
		}
	}

	return problems
}

// jobGets takes the gets the placement added out of one job, was the job without it.
func jobGets(job map[string]any, was any, declared map[string]bool) []string {
	name := fmt.Sprint(job["name"])
	gets := map[string]string{}

	eachMap(was, func(m map[string]any) {
		if g, ok := m["get"].(string); ok {
			gets[g] = canonical(fetchOf(m))
		}
	})

	var problems []string

	copies := map[string]string{}

	for key, v := range job {
		job[key] = without(v, func(m map[string]any) bool {
			g, ok := m["get"].(string)
			if !ok || gets[g] != "" {
				return false
			}

			if p := addedGet(name, m, gets, declared, copies); p != "" {
				problems = append(problems, p)
			}

			return true
		})
	}

	eachMap(job, func(m map[string]any) {
		if _, ok := m["task"]; !ok {
			return
		}

		if img, ok := m["image"].(string); ok && copies[img] != "" {
			m["image"] = copies[img]
		}

		if im, ok := m["input_mapping"].(map[string]any); ok {
			for in, from := range im {
				if s, ok := from.(string); ok && copies[s] != "" {
					im[in] = copies[s]
				}
			}
		}
	})

	return problems
}

// addedGet notes in copies which of the job's gets the added get copies, or says why it may not
// be added.
func addedGet(job string, get map[string]any, gets map[string]string, declared map[string]bool, copies map[string]string) string {
	g, res := fmt.Sprint(get["get"]), fetchOf(get)["resource"]

	switch {
	case !declared[fmt.Sprint(res)]:
		return fmt.Sprintf("job %s: get %s fetches %v, a resource the pipeline without the placement doesn't declare", job, g, res)
	case get["trigger"] == true:
		return fmt.Sprintf("job %s: get %s triggers the job", job, g)
	}

	for _, e := range slices.Sorted(maps.Keys(gets)) {
		if gets[e] == canonical(fetchOf(get)) {
			copies[g] = e

			return ""
		}
	}

	return fmt.Sprintf("job %s: get %s fetches %v as no get of the job does without the placement (passed, params or version differ)", job, g, res)
}

// fetchOf is what a get fetches, whatever its name and whether it triggers.
func fetchOf(get map[string]any) map[string]any {
	out := maps.Clone(get)
	if _, ok := out["resource"]; !ok {
		out["resource"] = out["get"]
	}

	delete(out, "get")
	delete(out, "trigger")

	return out
}

// idle takes out of every task's input_mapping what maps an input to its own name.
func idle(doc any) {
	eachMap(doc, func(m map[string]any) {
		im, ok := m["input_mapping"].(map[string]any)
		if _, task := m["task"]; !task || !ok {
			return
		}

		for in, from := range im {
			if from == in {
				delete(im, in)
			}
		}

		if len(im) == 0 {
			delete(m, "input_mapping")
		}
	})
}

func listAt(doc any, key string) []any {
	m, _ := doc.(map[string]any) //nolint:errcheck // a document that isn't a map has no list
	l, _ := m[key].([]any)       //nolint:errcheck // nor one whose key holds none

	return l
}

func eachMap(v any, f func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		f(x)

		for _, e := range x {
			eachMap(e, f)
		}
	case []any:
		for _, e := range x {
			eachMap(e, f)
		}
	}
}

// without is v with every map in a list that drop says to drop taken out.
func without(v any, drop func(map[string]any) bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = without(e, drop)
		}
	case []any:
		out := x[:0:0]

		for _, e := range x {
			if m, ok := e.(map[string]any); ok && drop(m) {
				continue
			}

			out = append(out, without(e, drop))
		}

		return out
	}

	return v
}

// differences are where two pipelines differ, by path: a list's entries by name or step where
// those are unique, else by place.
func differences(at string, was, is any) []string {
	switch w := was.(type) {
	case map[string]any:
		if i, ok := is.(map[string]any); ok {
			return mapDifferences(at, w, i)
		}
	case []any:
		if i, ok := is.([]any); ok {
			return listDifferences(at, w, i)
		}
	}

	if canonical(was) == canonical(is) {
		return nil
	}

	return []string{fmt.Sprintf("%s is %s without the placement, %s with it", orTop(at), brief(was), brief(is))}
}

func mapDifferences(at string, was, is map[string]any) []string {
	var out []string

	keys := slices.AppendSeq(slices.Collect(maps.Keys(was)), maps.Keys(is))
	slices.Sort(keys)

	for _, key := range slices.Compact(keys) {
		p := key
		if at != "" {
			p = at + "." + key
		}

		w, inW := was[key]
		i, inI := is[key]

		switch {
		case !inW:
			out = append(out, p+" only with the placement")
		case !inI:
			out = append(out, p+" only without it")
		default:
			out = append(out, differences(p, w, i)...)
		}
	}

	return out
}

func listDifferences(at string, was, is []any) []string {
	wl, il := labels(was), labels(is)
	if wl == nil || il == nil {
		if len(was) != len(is) {
			return []string{fmt.Sprintf("%s has %d entries without the placement, %d with it", orTop(at), len(was), len(is))}
		}

		var out []string
		for n := range was {
			out = append(out, differences(fmt.Sprintf("%s[%d]", at, n), was[n], is[n])...)
		}

		return out
	}

	var out []string

	for n, l := range wl {
		if j := slices.Index(il, l); j < 0 {
			out = append(out, at+"["+l+"] only without the placement")
		} else {
			out = append(out, differences(at+"["+l+"]", was[n], is[j])...)
		}
	}

	for _, l := range il {
		if !slices.Contains(wl, l) {
			out = append(out, at+"["+l+"] only with the placement")
		}
	}

	if len(out) == 0 && !slices.Equal(wl, il) {
		out = append(out, orTop(at)+" holds its entries in another order")
	}

	return out
}

// labels name a list's entries by their names or steps, or are nil where one has neither or two
// share one.
func labels(l []any) []string {
	out := make([]string, 0, len(l))

	for _, e := range l {
		m, ok := e.(map[string]any)
		if !ok {
			return nil
		}

		label := ""
		if n, ok := m["name"].(string); ok {
			label = n
		}

		for _, k := range []string{"get", "put", "task", "set_pipeline", "load_var"} {
			if s, ok := m[k].(string); ok && label == "" {
				label = k + " " + s
			}
		}

		if label == "" || slices.Contains(out, label) {
			return nil
		}

		out = append(out, label)
	}

	return out
}

func orTop(at string) string {
	if at == "" {
		return "the pipeline"
	}

	return at
}

func brief(v any) string {
	s := canonical(v)
	if len(s) > 60 {
		s = s[:57] + "..."
	}

	return s
}
