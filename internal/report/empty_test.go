package report

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

type emptyObserver struct{}

func (emptyObserver) Observe(context.Context, string, string, string, []string) ([]Observation, error) {
	return []Observation{{Plugin: "go"}, {Plugin: "sql"}}, nil
}

func endedWith(t *testing.T, allow bool, result string) (StateFile, recording.Recording, bool) {
	t.Helper()

	r := newReporter(t, bareRemote(t), newFake(), "list")
	r.Observer = emptyObserver{}

	if allow {
		s := r.Config.Suites["e2e"]
		s.EmptyRecordings = config.EmptyAllow
		r.Config.Suites["e2e"] = s
	}

	if err := WritePlan(t.Context(), r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	ev := SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Result: writeResult(t, result), Coverage: []string{t.TempDir(), t.TempDir()}}
	if _, err := r.Slice(t.Context(), ev); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if _, err := Collect(t.Context(), r.Store, r.ID, "e2e", out); err != nil {
		t.Fatal(err)
	}

	sf := readState(t, out+"/e2e.checkout/state.json")

	data, ok := readFileIf(t, out+"/e2e.checkout/recording.txt")
	if !ok {
		return sf, recording.Recording{}, false
	}

	rec, err := recording.Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	return sf, rec, true
}

func TestTestsThatRanAndRecordedNothingMarkTheSlice(t *testing.T) {
	sf, rec, ok := endedWith(t, false, `{"passed": true, "tests": 3}`)
	if !ok || rec.HasData() || len(rec.CannotTell) != 1 || rec.CannotTell[0].Reason != "3 tests ran and nothing was recorded" || rec.CannotTell[0].Since.IsZero() {
		t.Fatalf("recording %+v", rec)
	}

	if sf.State != Passed || len(sf.CannotTell) != 1 {
		t.Fatalf("state %+v", sf)
	}

	if _, rec, _ := endedWith(t, false, `{"passed": true}`); len(rec.CannotTell) != 1 || !strings.Contains(rec.CannotTell[0].Reason, "nothing was recorded") {
		t.Fatalf("uncounted: %+v", rec)
	}
}

func TestASliceThatRanNoTestsStillRecordsNothing(t *testing.T) {
	sf, _, ok := endedWith(t, false, `{"passed": true, "tests": 0}`)
	if ok || sf.State != Passed || sf.Tests == nil || *sf.Tests != 0 || len(sf.CannotTell) != 0 {
		t.Fatalf("state %+v, recorded %t", sf, ok)
	}
}

func TestASuiteThatAllowsEmptyRecordingsStoresTheEmptyList(t *testing.T) {
	sf, rec, ok := endedWith(t, true, `{"passed": true, "tests": 3}`)
	if !ok || !rec.HasData() || !rec.Observed || len(rec.Own) != 0 || len(rec.CannotTell) != 0 || sf.State != Passed {
		t.Fatalf("recording %+v %t, state %+v", rec, ok, sf)
	}
}

func readState(t *testing.T, p string) StateFile {
	t.Helper()

	data, ok := readFileIf(t, p)
	if !ok {
		t.Fatalf("no %s", p)
	}

	var sf StateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatal(err)
	}

	return sf
}

func readFileIf(t *testing.T, p string) ([]byte, bool) {
	t.Helper()

	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}

	if err != nil {
		t.Fatal(err)
	}

	return data, true
}
