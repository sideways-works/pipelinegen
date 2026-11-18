package kit

import (
	"context"
	"maps"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

const placementCheck = "a placement moves steps between workers and changes nothing else"

// staleHashParam holds a pipeline's hash of itself, which any change to
// it changes, a worker tag included.
const staleHashParam = "PIPELINEGEN_PIPELINE_SHA256"

// placement checks that the runner reads its placement from its own config and that one only moves
// steps between workers: the pipeline with it is the one without it but for worker tags.
func (k *kit) placement(ctx context.Context, root string, placed []byte) {
	const name = placementCheck

	k.check("describe says it reads its placement from its own config", ifNot(k.can(plugin.CapPlacement),
		"no "+plugin.CapPlacement+" capability: pipelinegen refuses a runner without it"))

	if _, ok := k.o.Config[config.PlacementKey]; !ok {
		k.skip(name, "the config the kit was given has no "+config.PlacementKey)

		return
	}

	bare := k.p
	bare.Config = maps.Clone(k.o.Config)
	delete(bare.Config, config.PlacementKey)

	work := k.work()

	var ans plugin.PipelineAnswer
	if err := k.call(ctx, plugin.CallPipeline, plugin.PipelineRequest{Request: bare.NewRequest(work), Spec: Spec(), Root: root}, &ans); err != nil || len(ans.Files) != 1 {
		k.check(name, "the pipeline without the placement: %v (%d files)", err, len(ans.Files))

		return
	}

	unplaced, err := os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))) //nolint:gosec // inside the call's work directory
	if err != nil {
		k.check(name, "%v", err)

		return
	}

	a, errA := untagged(placed)
	b, errB := untagged(unplaced)

	switch {
	case errA != nil || errB != nil:
		k.check(name, "a pipeline isn't YAML: %v %v", errA, errB)
	case a != b:
		k.check(name, "the pipelines with and without the placement differ in more than worker tags")
	case canonicalYAML(placed) == canonicalYAML(unplaced):
		k.check(name, "the placement moved no step: give the kit a placement that places one")
	default:
		k.check(name, "")
	}
}

func untagged(text []byte) (string, error) {
	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return "", err //nolint:wrapcheck // said with the check's name
	}

	return canonical(strip(doc)), nil
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
