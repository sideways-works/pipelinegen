package cli

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/durations"
)

func TestAnEstimateCountsAnUnmeasuredSliceAtTheMedian(t *testing.T) {
	file, err := durations.Parse([]byte(fmt.Sprintf(`{"version": %d, "files": {"a_test.go": 10, "b_test.go": 20, "c_test.go": 60}}`, durations.Version)))
	if err != nil {
		t.Fatal(err)
	}

	sl := func(name string) bazel.Slice { return bazel.Slice{Name: name, Sources: []string{name}} }
	all := []bazel.Slice{sl("a_test.go"), sl("b_test.go"), sl("c_test.go"), sl("new_test.go")}

	got := estimateOf(file, all, []bazel.Slice{sl("a_test.go"), sl("new_test.go")})
	want := affected.Estimate{SelectedSeconds: 30, AllSeconds: 110, Measured: 3, Slices: 4}

	if got == nil || *got != want {
		t.Errorf("estimate %+v, want %+v", got, want)
	}

	if got := estimateOf(durations.New(), all, all); got != nil {
		t.Errorf("an estimate with nothing measured: %+v", got)
	}
}

func TestAPlanIsWrittenWithItsVersion(t *testing.T) {
	data, err := json.Marshal(&affected.Plan{Version: 0, Head: "abc"})
	if err != nil {
		t.Fatal(err)
	}

	var back struct {
		Version int `json:"version"`
	}

	if err := json.Unmarshal(data, &back); err != nil || back.Version != affected.Version {
		t.Errorf("version %d in %s", back.Version, data)
	}
}
