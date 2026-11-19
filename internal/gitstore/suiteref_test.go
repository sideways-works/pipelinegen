package gitstore

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
)

func beganRef(suite string) string { return runsRoot + run + "/_began/" + suite }

func endOf(suite string) string { return runsRoot + run + "/_end/" + suite }

func TestSuitesStartingTogetherEachPushOnce(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	const suites = 6

	var (
		mu     sync.Mutex
		pushes = map[string]int{}
		wg     sync.WaitGroup
	)

	stores := make([]*Store, suites)
	for i := range stores {
		// each suite's tree step reads the plan first
		stores[i] = groupedStore(t, remote, "")
		if _, ok, err := stores[i].First(t.Context(), []string{planRef()}); err != nil || !ok {
			t.Fatalf("plan: %v %v", ok, err)
		}
	}

	for i, s := range stores {
		suite := "s" + strconv.Itoa(i)

		wg.Go(func() {
			calls := remoteCalls(t, func(ctx context.Context) {
				if _, err := s.Write(ctx, "began", Update{Ref: beganRef(suite), Any: true, Files: files("began.json", suite)}); err != nil {
					t.Error(err)
				}
			})

			mu.Lock()
			defer mu.Unlock()

			pushes[suite] = calls["push"]
		})
	}

	wg.Wait()

	for suite, n := range pushes {
		if n != 1 {
			t.Errorf("%s pushed %d times, want once: %v", suite, n, pushes)
		}
	}

	got := readAll(t, remote)

	for i := range suites {
		suite := "s" + strconv.Itoa(i)
		if got[beganRef(suite)] != "began.json="+suite {
			t.Errorf("%s's start: %v", suite, got)
		}
	}

	refs := remoteRefs(t, remote)
	for i := range suites {
		if own := BatchPrefix + run + "/s" + strconv.Itoa(i) + "/" + SuiteBatch; !slices.Contains(refs, own) {
			t.Errorf("no %s among %v", own, refs)
		}
	}
}

// endedRound is a run whose two suites began and ended, one with a batch's records.
func endedRound(t *testing.T, remote string) {
	t.Helper()

	plannedRun(t, remote, false)

	s := groupedStore(t, remote, "")

	for _, suite := range []string{"e2e", "go"} {
		for _, ref := range []string{beganRef(suite), endOf(suite)} {
			if _, err := s.Write(t.Context(), "mark", Update{Ref: ref, Any: true, Files: files("mark.json", ref)}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTwoLastEndsCompactingTogetherEndInOneState(t *testing.T) {
	once, twice := bare(t), bare(t)
	endedRound(t, once)
	endedRound(t, twice)

	before := readAll(t, twice)
	if len(before) != 6 {
		t.Fatalf("records %v", before)
	}

	if n, err := groupedStore(t, once, "").Compact(t.Context(), runsRoot+run+"/", ""); err != nil || n != 3 {
		t.Fatalf("one compaction moved %d refs: %v", n, err)
	}

	var wg sync.WaitGroup

	for range 2 {
		s := groupedStore(t, twice, "")

		wg.Go(func() {
			if _, err := s.Compact(t.Context(), runsRoot+run+"/", ""); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	for name, remote := range map[string]string{"one compaction": once, "two at once": twice} {
		if got := remoteRefs(t, remote); !slices.Equal(got, []string{RunPrefix + run}) {
			t.Errorf("%s left refs %v", name, got)
		}

		if got := readAll(t, remote); !maps.Equal(got, before) {
			t.Errorf("%s: records %v, want %v", name, got, before)
		}
	}
}
