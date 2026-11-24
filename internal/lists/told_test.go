package lists

import (
	"strconv"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"
)

func TestARunsRecordingsAskWhetherItPrecedesTheListsOnce(t *testing.T) {
	asked := 0
	precedes := remembered(func(_, _ string) (bool, bool) {
		asked++

		return true, true
	})

	l := recording.Lists{}
	for i := range 1000 {
		l["api/"+strconv.Itoa(i)+"_test.go"] = nil
	}

	told := toldSince(l, Source{From: "listscommit"}, precedes)
	run := LiveRun{Run: "main", Change: "main", Commit: "runcommit"}

	for key := range l {
		if !told(run, key) {
			t.Fatalf("%s: a run before the lists told it", key)
		}
	}

	if asked != 1 {
		t.Fatalf("asked git %d times for one run's 1000 recordings", asked)
	}
}
