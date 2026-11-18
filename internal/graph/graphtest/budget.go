package graphtest

import (
	"runtime"
	"sync/atomic"
	"time"
)

type Cost struct {
	Took   time.Duration
	Heap   uint64
	Allocs uint64
}

func (c Cost) MB() uint64 { return c.Heap >> 20 }

// Measure runs f once, sampling the heap every millisecond after a collection so the peak is f's own;
// a peak shorter than the interval can be missed.
func Measure(f func()) Cost {
	runtime.GC()

	var before runtime.MemStats

	runtime.ReadMemStats(&before)

	var peak atomic.Uint64

	done := make(chan struct{})
	sampled := make(chan struct{})

	go func() {
		defer close(sampled)

		var m runtime.MemStats

		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()

		for {
			runtime.ReadMemStats(&m)

			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}

			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()

	start := time.Now()

	f()

	took := time.Since(start)

	var after runtime.MemStats

	runtime.ReadMemStats(&after)

	close(done)
	<-sampled

	var heap uint64
	if p := peak.Load(); p > before.HeapAlloc {
		heap = p - before.HeapAlloc
	}

	return Cost{Took: took, Heap: heap, Allocs: after.Mallocs - before.Mallocs}
}
