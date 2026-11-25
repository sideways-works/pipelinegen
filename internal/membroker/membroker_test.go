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

func TestATaskAsksOnceAtATimeAndHoldsOneGrant(t *testing.T) {
	f := &fake{}
	f.free.Store(9000)

	b := &Broker{Read: f.read, Ramp: time.Minute, Every: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go b.Run(ctx)

	// a first ask from the task waits for room
	waiting := make(chan Answer, 1)

	go func() { waiting <- b.ask(t.Context(), Request{MB: 12000, WaitSeconds: 0.3}, "10.80.0.7") }()

	time.Sleep(50 * time.Millisecond)

	if ans := b.ask(t.Context(), Request{MB: 1000, WaitSeconds: 1}, "10.80.0.7"); ans.Granted || ans.Why != busy {
		t.Fatalf("a second ask at once: %+v", ans)
	}

	<-waiting

	// granted twice in a row, the task holds its last grant alone
	for range 2 {
		if ans := b.ask(t.Context(), Request{MB: 4000, WaitSeconds: 1}, "10.80.0.7"); !ans.Granted {
			t.Fatalf("%+v", ans)
		}
	}

	if ans := b.ask(t.Context(), Request{MB: 5000, WaitSeconds: 1}, "10.80.0.8"); !ans.Granted || ans.HeldMB != 9000 {
		t.Fatalf("another task: %+v", ans)
	}
}

func TestAGrantCountsAtMostTheCap(t *testing.T) {
	f := &fake{}
	f.free.Store(9000)

	b := &Broker{Read: f.read, Ramp: time.Minute, Every: 10 * time.Millisecond, MaxGrant: 8000}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go b.Run(ctx)

	if ans := b.Ask(t.Context(), Request{MB: 12000, WaitSeconds: 1}); !ans.Granted || ans.HeldMB != 8000 {
		t.Fatalf("%+v", ans)
	}
}

// cpuBroker runs a broker of 8000 MB free, with cores and a CPU pressure the test sets.
func cpuBroker(t *testing.T, cpus, hold float64) (*Broker, *atomic.Int64) {
	t.Helper()

	f := &fake{}
	f.free.Store(8000)

	var psi atomic.Int64

	b := &Broker{Read: f.read, Ramp: time.Minute, Every: 10 * time.Millisecond, CPUs: cpus, CPUHold: hold, ReadCPU: func() float64 { return float64(psi.Load()) }}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go b.Run(ctx)

	return b, &psi
}

func TestCoresHeldBlockASecondAskUntilTheFirstsLeaseEndsOrItReleases(t *testing.T) {
	for _, tc := range []struct {
		name string
		// end ends the first grant's cores: its lease runs out, or its batch releases them
		lease float64
		end   func(b *Broker)
	}{
		{"the lease ends", 0.2, func(*Broker) {}},
		{"the first releases", 60, func(b *Broker) { b.drop(Request{Release: true, Who: "first"}, "10.80.0.7") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := cpuBroker(t, 4, 0)

			first := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 1, Who: "first", CPU: true, Cores: 3, LeaseSeconds: tc.lease}, "10.80.0.7")
			if !first.Granted || first.HeldCores != 3 || first.FreeCores != 4 {
				t.Fatalf("first: %+v", first)
			}

			// 3 of 4 cores held: 2 more wait, and are refused when their wait ends first
			if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 0.05, Who: "second", CPU: true, Cores: 2}, "10.80.0.8"); ans.Granted ||
				!strings.Contains(ans.Why, "3.0 of 4.0 cores granted lately, CPU pressure 0.0%, for 2.0 cores asked") {
				t.Fatalf("second while the first holds its cores: %+v", ans)
			}

			tc.end(b)

			if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 1, Who: "second", CPU: true, Cores: 2}, "10.80.0.8"); !ans.Granted || ans.HeldCores != 2 {
				t.Fatalf("second once the first's cores ended: %+v", ans)
			}
		})
	}
}

func TestAnAskForMoreCoresThanTheWorkerHasStartsAtOnce(t *testing.T) {
	b, _ := cpuBroker(t, 4, 0)

	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 1, CPU: true, Cores: 3}, "10.80.0.7"); !ans.Granted {
		t.Fatalf("%+v", ans)
	}

	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 0.5, CPU: true, Cores: 6}, "10.80.0.8"); !ans.Granted || ans.HeldCores != 9 {
		t.Fatalf("more than the worker has: %+v", ans)
	}

	// an ask of no cores waits for the pressure alone, though the cores are overbooked
	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 0.5, CPU: true}, "10.80.0.9"); !ans.Granted {
		t.Fatalf("no cores: %+v", ans)
	}
}

func TestCPUPressureHoldsBackAsksThatWaitForCPUAlone(t *testing.T) {
	b, psi := cpuBroker(t, 0, 40)
	psi.Store(55)

	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 0.05, CPU: true}, "10.80.0.7"); ans.Granted || !strings.Contains(ans.Why, "CPU pressure 55.0%") {
		t.Fatalf("under CPU pressure: %+v", ans)
	}

	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 1}, "10.80.0.8"); !ans.Granted || strings.Contains(ans.Why, "cores") {
		t.Fatalf("an ask that doesn't wait for CPU: %+v", ans)
	}

	// an unreadable pressure holds nothing back
	psi.Store(-1)

	if ans := b.ask(t.Context(), Request{MB: 100, WaitSeconds: 1, CPU: true}, "10.80.0.7"); !ans.Granted {
		t.Fatalf("an unreadable pressure: %+v", ans)
	}
}

func TestAReleaseDropsItsSourcesCoresAndKeepsItsMemory(t *testing.T) {
	b, _ := cpuBroker(t, 8, 0)

	for _, from := range []string{"10.80.0.7", "10.80.0.8"} {
		if ans := b.ask(t.Context(), Request{MB: 1000, WaitSeconds: 1, Who: from, CPU: true, Cores: 2, LeaseSeconds: 60}, from); !ans.Granted {
			t.Fatalf("%s: %+v", from, ans)
		}
	}

	if ans := b.drop(Request{Release: true, Who: "any name"}, "10.80.0.7"); !ans.Granted || ans.HeldCores != 2 || ans.FreeCores != 6 {
		t.Fatalf("release: %+v", ans)
	}

	// the worker's host itself is no source: its release names its grant
	if ans := b.ask(t.Context(), Request{MB: 1000, WaitSeconds: 1, Who: "host batch", CPU: true, Cores: 3, LeaseSeconds: 60}, ""); !ans.Granted || ans.HeldCores != 5 {
		t.Fatalf("host: %+v", ans)
	}

	if ans := b.drop(Request{Release: true, Who: "another"}, ""); ans.HeldCores != 5 {
		t.Fatalf("a release of another name: %+v", ans)
	}

	if ans := b.drop(Request{Release: true, Who: "host batch"}, ""); ans.HeldCores != 2 {
		t.Fatalf("the host batch's release: %+v", ans)
	}

	b.mu.Lock()
	mb, cores := b.held(time.Now())
	b.mu.Unlock()

	if mb != 3000 || cores != 2 {
		t.Fatalf("held %d MB, %v cores: the released grants' memory still ramps", mb, cores)
	}
}

func TestAReleaseOverTheNetworkNeverWaits(t *testing.T) {
	f := &fake{}
	f.free.Store(100)
	addr := serve(t, f, time.Minute)

	// the memory covers no ask now, and a release still answers at once
	start := time.Now()
	if err := Release(t.Context(), addr, "e2e batch 1"); err != nil || time.Since(start) > time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}

	if ans, err := Ask(t.Context(), addr, Request{MB: 1, WaitSeconds: 1, Cores: -1}); err != nil || ans.Granted {
		t.Fatalf("negative cores: %+v %v", ans, err)
	}
}
