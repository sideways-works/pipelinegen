// Package membroker hands out a worker's memory to the batches that start on it, one at a time and in
// the order they asked: a batch asks for what its slices' recorded peaks predict, and the broker grants
// it once the worker's free memory, less what it granted lately, covers it. A grant counts against the
// worker's memory until its ramp ends, by when the batch's own use shows in the free memory itself.
//
// Batches of one worker share no store and no file: the broker runs beside the worker, and a batch
// reaches it over the network, its address in memory.broker. A batch that can't reach it waits by its
// own reading of the worker's memory, as without a broker.
package membroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/memory"
)

// Request is a batch's ask: MB of memory, waiting at most WaitSeconds; Who names it in the broker's log.
type Request struct {
	MB          int64   `json:"mb"`
	WaitSeconds float64 `json:"wait_seconds"`
	Who         string  `json:"who"`
}

// Answer is the broker's: Granted, or Why not.
type Answer struct {
	Granted bool   `json:"granted"`
	FreeMB  int64  `json:"free_mb"`
	HeldMB  int64  `json:"held_mb"`
	Why     string `json:"why,omitempty"`
}

// admitPressure is the memory pressure (PSI "some avg10", percent) at or above which nothing is granted.
const admitPressure = 10

// Broker grants memory in the order it was asked for.
type Broker struct {
	// Read reads the worker's memory now: what it has free and the pressure on it.
	Read func() (memory.Reading, float64, error)
	// Ramp is how long a grant counts against the worker's free memory.
	Ramp time.Duration
	// Every is how often a waiting ask reads the memory again.
	Every time.Duration
	// Log, when set, gets a line per grant and per refusal.
	Log io.Writer
	// FirstFor is how long the first ask in line holds back the asks after it: past it, an ask behind
	// it that the memory covers goes first, so one ask for most of the worker, from a proposed
	// change's batch say, delays the others at most this long. 0 holds them back as long as it waits.
	FirstFor time.Duration

	mu      sync.Mutex
	queue   []*ask
	granted []grant
	wake    chan struct{}
	conns   chan struct{}
	once    sync.Once
}

type ask struct {
	req  Request
	done chan Answer
	at   time.Time
}

type grant struct {
	mb    int64
	until time.Time
}

func (b *Broker) init() {
	b.once.Do(func() {
		b.wake = make(chan struct{}, 1)
		b.conns = make(chan struct{}, maxConns)
	})
}

func (b *Broker) slots() chan struct{} {
	b.init()

	return b.conns
}

// held is what the grants still ramping hold, dropping the ended ones.
func (b *Broker) held(now time.Time) int64 {
	var sum int64

	kept := b.granted[:0]

	for _, g := range b.granted {
		if now.Before(g.until) {
			kept = append(kept, g)
			sum += g.mb
		}
	}

	b.granted = kept

	return sum
}

// Ask queues req and waits for its answer: granted once it is first in line and the memory covers it,
// refused when its wait ends first or ctx ends.
func (b *Broker) Ask(ctx context.Context, req Request) Answer {
	b.init()

	a := &ask{req: req, done: make(chan Answer, 1), at: time.Now()}

	b.mu.Lock()
	b.queue = append(b.queue, a)
	b.mu.Unlock()
	b.poke()

	wait := time.Duration(req.WaitSeconds * float64(time.Second))
	timer := time.NewTimer(wait)

	defer timer.Stop()

	select {
	case ans := <-a.done:
		return ans
	case <-timer.C:
	case <-ctx.Done():
	}

	// gone from the queue unless it was granted meanwhile
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case ans := <-a.done:
		return ans
	default:
	}

	for i, q := range b.queue {
		if q == a {
			b.queue = append(b.queue[:i], b.queue[i+1:]...)

			break
		}
	}

	b.poke()

	r, pressure, _ := b.Read() //nolint:errcheck // a refusal says what it read, or nothing
	held := b.held(time.Now())
	ans := Answer{FreeMB: r.Free() - held, HeldMB: held, Why: fmt.Sprintf("%d MB free less %d MB granted lately, pressure %.1f%%, for %d MB asked", r.Free(), held, pressure, req.MB)}
	b.logf("refused %q: %s after %s\n", req.Who, ans.Why, wait.Round(time.Second))

	return ans
}

func (b *Broker) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Run grants the queue's first ask whenever the memory covers it, until ctx ends.
func (b *Broker) Run(ctx context.Context) {
	b.init()

	every := b.Every
	if every <= 0 {
		every = 2 * time.Second
	}

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		b.step()

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-b.wake:
		}
	}
}

// step grants asks in their order while the memory covers them; once the first has held the others back
// for FirstFor, the first after it that the memory covers goes ahead of it.
func (b *Broker) step() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		r, pressure, err := b.Read()
		now := time.Now()
		held := b.held(now)

		i := b.next(r, pressure, err, held, now)
		if i < 0 {
			return
		}

		a := b.queue[i]
		b.queue = append(b.queue[:i], b.queue[i+1:]...)
		b.granted = append(b.granted, grant{mb: a.req.MB, until: now.Add(b.Ramp)})

		ans := Answer{Granted: true, FreeMB: r.Free() - held, HeldMB: held + a.req.MB}
		b.logf("granted %q %d MB: %d MB free less %d MB granted lately\n", a.req.Who, a.req.MB, r.Free(), held)

		a.done <- ans
	}
}

// next is the queue's index of the ask to grant now, -1 for none.
func (b *Broker) next(r memory.Reading, pressure float64, err error, held int64, now time.Time) int {
	fits := func(a *ask) bool {
		switch {
		case err != nil:
			// a worker whose memory can't be read grants as a batch without a broker starts: unchecked
			return true
		case a.req.MB > r.Capacity():
			// an ask no worker could ever cover starts at once, rather than wait for room never coming
			return true
		default:
			return r.Free()-held >= a.req.MB && pressure < admitPressure
		}
	}

	for i, a := range b.queue {
		if fits(a) {
			return i
		}

		// the first in line holds the others back until FirstFor has passed
		if i == 0 && (b.FirstFor <= 0 || now.Sub(a.at) < b.FirstFor) {
			return -1
		}
	}

	return -1
}

func (b *Broker) logf(format string, args ...any) {
	if b.Log != nil {
		_, _ = fmt.Fprintf(b.Log, time.Now().UTC().Format(time.RFC3339)+" "+format, args...) //nolint:errcheck // a log line
	}
}

// Serve answers each connection's one request, until ctx ends.
func (b *Broker) Serve(ctx context.Context, l net.Listener) error {
	go b.Run(ctx)

	go func() {
		<-ctx.Done()

		_ = l.Close() //nolint:errcheck // the listener goes with the broker
	}()

	for {
		c, err := l.Accept()

		switch {
		case err != nil && ctx.Err() != nil:
			return nil //nolint:nilerr // the listener closed as the broker ended
		case err != nil:
			return fmt.Errorf("membroker: %w", err)
		}

		select {
		case b.slots() <- struct{}{}:
		case <-ctx.Done():
			_ = c.Close() //nolint:errcheck // the broker ends

			return nil
		}

		go func() {
			defer func() { <-b.slots() }()

			b.handle(ctx, c)
		}()
	}
}

// What a request may hold: a request comes from any batch on the worker, a proposed change's among them.
const (
	// maxRequest bounds a request line: a few numbers and a name.
	maxRequest = 4096
	maxWho     = 256
	// maxWait is the longest wait a request may ask for: a suite's time limit is shorter.
	maxWait = 24 * time.Hour
	// maxConns bounds the connections served at once; more wait to be accepted.
	maxConns = 512
)

func (b *Broker) handle(ctx context.Context, c net.Conn) {
	defer c.Close() //nolint:errcheck // the answer was written or the batch went

	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck // a slow batch is cut off

	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}

	var req Request
	if json.Unmarshal(line, &req) != nil || req.MB < 0 || req.WaitSeconds < 0 || req.WaitSeconds > maxWait.Seconds() || len(req.Who) > maxWho {
		answer(c, Answer{Why: "a request is one line of JSON: {\"mb\": <n>, \"wait_seconds\": <n>, \"who\": \"...\"}"})

		return
	}

	_ = c.SetReadDeadline(time.Time{}) //nolint:errcheck // no more is read

	// the batch's going ends its wait: a closed connection reads EOF
	gone, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_, _ = c.Read(make([]byte, 1)) //nolint:errcheck // any read's end is the batch's going

		cancel()
	}()

	ans := b.Ask(gone, req)

	answer(c, ans)
}

// answer writes ans to the batch, which may be gone: it then takes no answer.
func answer(c net.Conn, ans Answer) {
	data, err := json.Marshal(ans)
	if err != nil {
		return
	}

	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck // a batch gone takes no answer
	_, _ = c.Write(append(data, '\n'))                       //nolint:errcheck // as above
}

// ErrUnreachable is a broker the batch couldn't reach or that answered nothing it reads.
var ErrUnreachable = errors.New("memory broker unreachable")

// Ask asks the broker at addr for mb, waiting at most wait, and returns its answer.
func Ask(ctx context.Context, addr string, req Request) (Answer, error) {
	d := net.Dialer{Timeout: 5 * time.Second}

	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Answer{}, fmt.Errorf("%w: %s: %w", ErrUnreachable, addr, err)
	}
	defer c.Close() //nolint:errcheck // one request, one answer

	data, err := json.Marshal(req)
	if err != nil {
		return Answer{}, fmt.Errorf("membroker: %w", err)
	}

	if _, err := c.Write(append(data, '\n')); err != nil {
		return Answer{}, fmt.Errorf("%w: %s: %w", ErrUnreachable, addr, err)
	}

	// the broker answers within the wait, and a little more for its last reading
	_ = c.SetReadDeadline(time.Now().Add(time.Duration(req.WaitSeconds*float64(time.Second)) + 30*time.Second)) //nolint:errcheck // set on a fresh connection

	stop := context.AfterFunc(ctx, func() { _ = c.Close() }) //nolint:errcheck // ctx's end ends the wait
	defer stop()

	var ans Answer
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&ans); err != nil {
		if ctx.Err() != nil {
			return Answer{}, fmt.Errorf("membroker: %w", ctx.Err())
		}

		return Answer{}, fmt.Errorf("%w: %s: %w", ErrUnreachable, addr, err)
	}

	return ans, nil
}
