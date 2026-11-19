package gitstore

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

func TestAWriteToARunItHasReadAsksNothingFirst(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	s := groupedStore(t, remote, "")
	if _, ok, err := s.First(t.Context(), []string{planRef()}); err != nil || !ok {
		t.Fatalf("plan: %v %v", ok, err)
	}

	calls := remoteCalls(t, func(ctx context.Context) {
		if _, err := s.Write(ctx, "end", Update{Ref: endRef(), Any: true, Files: files("end.json", "{}")}); err != nil {
			t.Fatal(err)
		}
	})

	if calls["ls-remote"] != 0 || calls["fetch"] != 1 || calls["push"] != 1 {
		t.Errorf("a write after reading its run reached the remote %v, want one fetch and one push", calls)
	}

	if got := readAll(t, remote)[endRef()]; got != "end.json={}" {
		t.Errorf("end %q", got)
	}
}

func TestWritersThatLoseARaceTryAgainWithoutAsking(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	const writers = 6

	var (
		mu    sync.Mutex
		total = map[string]int{}
		wg    sync.WaitGroup
	)

	stores := make([]*Store, writers)
	for i := range stores {
		stores[i] = groupedStore(t, remote, "")
		if _, ok, err := stores[i].First(t.Context(), []string{planRef()}); err != nil || !ok {
			t.Fatalf("plan: %v %v", ok, err)
		}
	}

	for i, s := range stores {
		wg.Go(func() {
			calls := remoteCalls(t, func(ctx context.Context) {
				if _, err := s.Write(ctx, "began", Update{Ref: runsRoot + run + "/_began/s" + strconv.Itoa(i), Any: true, Files: files("began.json", strconv.Itoa(i))}); err != nil {
					t.Error(err)
				}
			})

			mu.Lock()
			defer mu.Unlock()

			for k, n := range calls {
				total[k] += n
			}
		})
	}

	wg.Wait()

	t.Logf("%d writers: %v", writers, total)

	if total["ls-remote"] != 0 || total["push"] < writers || total["fetch"] != total["push"] {
		t.Errorf("%d writers reached the remote %v, want a fetch and a push a try and nothing else", writers, total)
	}

	got := readAll(t, remote)
	for i := range writers {
		if got[runsRoot+run+"/_began/s"+strconv.Itoa(i)] != "began.json="+strconv.Itoa(i) {
			t.Errorf("writer %d's record: %v", i, got)
		}
	}
}

func TestAWriteToARefGoneSinceItWasSeenStillLands(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	// a batch's ref, seen, then moved into the run's ref and deleted by the suite's end
	s := groupedStore(t, remote, "1")
	if _, err := s.List(t.Context(), runsRoot+run+"/"); err != nil {
		t.Fatal(err)
	}

	if _, err := groupedStore(t, remote, "").Compact(t.Context(), runsRoot+run+"/", "e2e"); err != nil {
		t.Fatal(err)
	}

	state(t, s, "b.spec.ts", "passed")

	got := readAll(t, remote)
	if got[stateRef("a.spec.ts")] == "" || got[stateRef("b.spec.ts")] != "recording.txt=units of b.spec.ts,state.json=passed" {
		t.Errorf("records %v", got)
	}
}
