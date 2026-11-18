package kit

import (
	_ "embed"
	"encoding/json"

	"github.com/sideways-works/pipelinegen/plugin"
)

// specJSON is the spec of the fixture repository's config (testdata/kit), which every runner must
// turn into a pipeline.
//
//go:embed spec.json
var specJSON []byte

func Spec() plugin.Spec {
	var s plugin.Spec
	if err := json.Unmarshal(specJSON, &s); err != nil {
		panic("the kit's fixture spec: " + err.Error())
	}

	return s
}
