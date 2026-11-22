package affected

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

type schemaDef struct {
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// TestThePlanSchemaIsThePlansJSON holds docs/reference/plan.schema.json to the Go types the plan is
// written from: each definition has exactly the type's fields, and requires those always written.
func TestThePlanSchemaIsThePlansJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "plan.schema.json"))
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Defs map[string]schemaDef `json:"$defs"`
	}

	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	types := map[string]any{
		"Plan": Plan{}, "SuitePlan": SuitePlan{}, "Slice": bazel.Slice{}, "Reason": Reason{}, "Hop": Hop{},
		"Cause": Cause{}, "Narrowed": Narrowed{}, "Estimate": Estimate{}, "Override": plugins.Override{},
		"Report": plugins.Report{}, "CallCount": plugins.CallCount{}, "Source": lists.Source{},
		"LiveRun": lists.LiveRun{}, "Mark": recording.Mark{}, "Rebuild": Rebuild{},
		"Difference": plugins.Difference{}, "Unobservable": Unobservable{}, "Part": config.Part{},
	}

	for name := range schema.Defs {
		if _, ok := types[name]; !ok {
			t.Errorf("the schema defines %s, which no type of the plan is", name)
		}
	}

	for name, v := range types {
		def, ok := schema.Defs[name]
		if !ok {
			t.Errorf("the schema has no %s", name)

			continue
		}

		fields, required := jsonFields(reflect.TypeOf(v))

		props := make([]string, 0, len(def.Properties))
		for p := range def.Properties {
			props = append(props, p)
		}

		slices.Sort(props)
		slices.Sort(def.Required)

		if !slices.Equal(props, fields) {
			t.Errorf("%s: the schema's properties %v, the type's fields %v", name, props, fields)
		}

		if !slices.Equal(def.Required, required) {
			t.Errorf("%s: the schema requires %v, the type always writes %v", name, def.Required, required)
		}
	}
}

func jsonFields(t reflect.Type) (fields, required []string) {
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" || tag == "" {
			continue
		}

		name, opts, _ := strings.Cut(tag, ",")
		fields = append(fields, name)

		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") && f.Type != reflect.TypeFor[time.Time]() {
			required = append(required, name)
		}
	}

	slices.Sort(fields)
	slices.Sort(required)

	return fields, required
}
