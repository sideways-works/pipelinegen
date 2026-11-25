package membroker

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/memory"
)

// fake is a worker whose free memory the test sets.
type fake struct{ free atomic.Int64 }

func (f *fake) read() (memory.Reading, float64, error) {
	return memory.Reading{Available: f.free.Load(), Total: 16000}, 0, nil
}

func serve(t *testing.T, f *fake, ramp time.Duration) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	b := &Broker{Read: f.read, Ramp: ramp, Every: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = b.Serve(ctx, l) }() //nolint:errcheck // ends with the test

	return l.Addr().String()
}

// Two batches asking at once for what only one fits: the first is granted, the second waits until the
// first's grant has ramped out and the memory it reads covers it.
func TestBatchesAskingAtOnceAreGrantedOneAfterTheOther(t *testing.T) {
	f := &fake{}
	f.free.Store(8000)
	addr := serve(t, f, 200*time.Millisecond)

	var (
		wg    sync.WaitGroup
		order []string
		mu    sync.Mutex
	)

	for _, who := range []string{"a", "b"} {
		wg.Go(func() {
			ans, err := Ask(t.Context(), addr, Request{MB: 6000, WaitSeconds: 5, Who: who})
			if err != nil || !ans.Granted {
				t.Errorf("%s: %+v %v", who, ans, err)
			}

			mu.Lock()

			order = append(order, who)

			mu.Unlock()
		})

		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if len(order) != 1 {
		t.Fatalf("granted %v before the first grant ramped out", order)
	}
	mu.Unlock()

	wg.Wait()

	if strings.Join(order, " ") != "a b" {
		t.Fatalf("order %v", order)
	}
}

func TestAnAskTheMemoryNeverCoversIsRefusedWhenItsWaitEnds(t *testing.T) {
	f := &fake{}
	f.free.Store(3000)
	addr := serve(t, f, time.Minute)

	ans, err := Ask(t.Context(), addr, Request{MB: 6000, WaitSeconds: 0.2, Who: "big"})
	if err != nil || ans.Granted || !strings.Contains(ans.Why, "3000 MB free less 0 MB granted lately") {
		t.Fatalf("%+v %v", ans, err)
	}

	// one more than the worker could ever hold starts at once
	ans, err = Ask(t.Context(), addr, Request{MB: 20000, WaitSeconds: 0.2, Who: "huge"})
	if err != nil || !ans.Granted {
		t.Fatalf("huge: %+v %v", ans, err)
	}
}

func TestAnAskWaitsWhileTheFirstInLineWaits(t *testing.T) {
	f := &fake{}
	f.free.Store(5000)
	addr := serve(t, f, time.Minute)

	big := make(chan Answer, 1)

	go func() {
		ans, _ := Ask(t.Context(), addr, Request{MB: 6000, WaitSeconds: 5, Who: "big"}) //nolint:errcheck // checked by its answer
		big <- ans
	}()

	time.Sleep(50 * time.Millisecond)

	// a small ask behind a big one waits its turn, so the big one isn't starved
	small, err := Ask(t.Context(), addr, Request{MB: 1000, WaitSeconds: 0.2, Who: "small"})
	if err != nil || small.Granted {
		t.Fatalf("small went first: %+v %v", small, err)
	}

	f.free.Store(7000)

	if ans := <-big; !ans.Granted {
		t.Fatalf("big: %+v", ans)
	}
}

func TestAnUnreachableBrokerSaysSo(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close() //nolint:errcheck // nothing listens there now

	if _, err := Ask(t.Context(), addr, Request{MB: 1, WaitSeconds: 1}); err == nil || !strings.Contains(err.Error(), "memory broker unreachable") {
		t.Fatal(err)
	}
}

func TestTheFirstInLineHoldsTheOthersBackOnlyForAWhile(t *testing.T) {
	f := &fake{}
	f.free.Store(5000)

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	b := &Broker{Read: f.read, Ramp: time.Minute, Every: 10 * time.Millisecond, FirstFor: 150 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = b.Serve(ctx, l) }() //nolint:errcheck // ends with the test

	addr := l.Addr().String()

	go func() {
		_, _ = Ask(t.Context(), addr, Request{MB: 15000, WaitSeconds: 5, Who: "most of it"}) //nolint:errcheck // never granted here
	}()

	time.Sleep(30 * time.Millisecond)

	start := time.Now()

	small, err := Ask(t.Context(), addr, Request{MB: 1000, WaitSeconds: 5, Who: "small"})
	if err != nil || !small.Granted {
		t.Fatalf("small: %+v %v", small, err)
	}

	if waited := time.Since(start); waited < 80*time.Millisecond {
		t.Fatalf("small went first after %s, before the first in line's while", waited)
	}
}

func TestARequestOutOfBoundsIsRefusedUnread(t *testing.T) {
	f := &fake{}
	f.free.Store(8000)
	addr := serve(t, f, time.Minute)

	for name, req := range map[string]Request{
		"a negative ask":   {MB: -1, WaitSeconds: 1},
		"a wait of a week": {MB: 1, WaitSeconds: 7 * 24 * 3600},
		"a long name":      {MB: 1, WaitSeconds: 1, Who: strings.Repeat("x", 300)},
	} {
		ans, err := Ask(t.Context(), addr, req)
		if err != nil || ans.Granted || !strings.Contains(ans.Why, "a request is one line of JSON") {
			t.Errorf("%s: %+v %v", name, ans, err)
		}
	}
}
