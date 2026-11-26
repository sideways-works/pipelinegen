// Package membroker hands out a worker's memory to the batches that start on it, one at a time and in
// the order they asked: a batch asks for what its slices' recorded peaks predict, and the broker grants
// it once the worker's free memory, less what it granted lately, covers it. A grant counts against the
// worker's memory until its ramp ends, by when the batch's own use shows in the free memory itself.
//
// Under cpu.wait a batch asks for its cores too: the broker grants it once the worker's CPU pressure is
// below its hold and the cores its grants' leases hold leave room for the batch's. A grant's cores count
// until the batch releases them at its end, until its task is gone, or until its lease, the batch's
// expected time and half again, runs out. A batch that waits before its setup (slice memory) releases
// nothing itself: its cores count while its task answers on the worker's network.
//
// Batches of one worker share no store and no file: the broker runs beside the worker, and a batch
// reaches it over the network, its address in memory.broker. A batch that can't reach it waits by its
// own reading of the worker's memory, as without a broker.
package membroker

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/memory"
)

// Request is a batch's ask: MB of memory, waiting at most WaitSeconds; Who names it in the broker's log.
type Request struct {
	MB          int64   `json:"mb"`
	WaitSeconds float64 `json:"wait_seconds"`
	Who         string  `json:"who"`
	// CPU makes the ask wait for the worker's CPU too: its pressure below the broker's hold, and room
	// for Cores among the cores the grants' leases hold. An ask of no cores waits for the pressure alone.
	CPU   bool    `json:"cpu,omitempty"`
	Cores float64 `json:"cores,omitempty"`
	// LeaseSeconds is how long the grant's cores count unless released, 0 for as long as its memory.
	LeaseSeconds float64 `json:"lease_seconds,omitempty"`
	// Release says the task's batch has ended: its grant's cores count no longer, its memory still does.
	Release bool `json:"release,omitempty"`
}

// Answer is the broker's: Granted, or Why not.
type Answer struct {
	Granted bool   `json:"granted"`
	FreeMB  int64  `json:"free_mb"`
	HeldMB  int64  `json:"held_mb"`
	Why     string `json:"why,omitempty"`
	// HeldCores and FreeCores are the cores the grants' leases hold and those left, under core accounting.
	HeldCores float64 `json:"held_cores,omitempty"`
	FreeCores float64 `json:"free_cores,omitempty"`
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
	// MaxGrant is the most one grant counts against the free memory, in MB, and the most an ask waits
	// for: a batch predicted above it starts once that much is free. 0 is no cap.
	MaxGrant int64
	// CPUs is the cores the grants of asks that wait for CPU may hold at once: an ask is granted once
	// its cores fit beside theirs, and one for more than all of them at once. 0 counts no cores.
	CPUs float64
	// CPUHold is the CPU pressure (PSI "some avg10", percent) at or above which no ask that waits for
	// CPU is granted; 0 holds none back.
	CPUHold float64
	// ReadCPU reads the worker's CPU pressure now, -1 where it can't; nil reads none.
	ReadCPU func() float64
	// Gone says whether the task at a grant's address has ended. A grant whose task is gone on two
	// probes in a row holds its cores no longer, its memory counting until its ramp ends. nil probes
	// none, and a grant's cores count until its release or its lease's end.
	Gone func(ctx context.Context, from string) bool
	// ProbeEvery is how often the tasks of the grants holding cores are probed, 30 seconds when 0.
	ProbeEvery time.Duration

	mu      sync.Mutex
	pending map[string]bool
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
	// from is the asking task's address: one ask at a time from it, and its grant replaces its last.
	from string
}

type grant struct {
	mb    int64
	until time.Time
	from  string
	who   string
	// cores count until coresUntil, or until the batch releases them.
	cores      float64
	coresUntil time.Time
	// missed is how many probes in a row found the grant's task gone.
	missed int
}

// reading is what the broker reads at once: the memory and its pressure, the CPU pressure, and what
// the grants hold.
type reading struct {
	r        memory.Reading
	pressure float64
	err      error
	cpu      float64
	held     int64
	cores    float64
}

func (b *Broker) read(now time.Time) reading {
	var st reading

	st.r, st.pressure, st.err = b.Read()

	st.cpu = -1
	if b.ReadCPU != nil {
		st.cpu = b.ReadCPU()
	}

	st.held, st.cores = b.held(now)

	return st
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

// held is the memory the grants still ramping hold and the cores those still leased hold, dropping the
// grants that do neither.
func (b *Broker) held(now time.Time) (int64, float64) {
	var (
		mb    int64
		cores float64
	)

	kept := b.granted[:0]

	for _, g := range b.granted {
		ramping, leased := now.Before(g.until), g.cores > 0 && now.Before(g.coresUntil)
		if !ramping && !leased {
			continue
		}

		kept = append(kept, g)

		if ramping {
			mb += g.mb
		}

		if leased {
			cores += g.cores
		}
	}

	b.granted = kept

	return mb, cores
}

// freeCores is the cores left beside those held, 0 without core accounting.
func (b *Broker) freeCores(cores float64) float64 {
	return max(b.CPUs-cores, 0)
}

// cpuSaid is what the broker read of the CPU, for a refusal of an ask that waits for it.
func (b *Broker) cpuSaid(st reading, req Request) string {
	psi := "unknown"
	if st.cpu >= 0 {
		psi = fmt.Sprintf("%.1f%%", st.cpu)
	}

	return fmt.Sprintf("; %.1f of %.1f cores granted lately, CPU pressure %s, for %.1f cores asked", st.cores, b.CPUs, psi, req.Cores)
}

// Ask queues req and waits for its answer: granted once it is first in line and the memory covers it,
// refused when its wait ends first or ctx ends.
func (b *Broker) Ask(ctx context.Context, req Request) Answer {
	return b.ask(ctx, req, "")
}

// busy is why a second ask from an address whose first still waits is refused.
const busy = "one ask at a time from a task: its first still waits"

// take marks from as waiting, false when it is already; b.mu is held.
func (b *Broker) take(from string) bool {
	if from == "" {
		return true
	}

	if b.pending[from] {
		return false
	}

	if b.pending == nil {
		b.pending = map[string]bool{}
	}

	b.pending[from] = true

	return true
}

func (b *Broker) release(from string) {
	if from == "" {
		return
	}

	b.mu.Lock()
	delete(b.pending, from)
	b.mu.Unlock()
}

func (b *Broker) ask(ctx context.Context, req Request, from string) Answer {
	b.init()

	if b.MaxGrant > 0 {
		req.MB = min(req.MB, b.MaxGrant)
	}

	a := &ask{req: req, done: make(chan Answer, 1), at: time.Now(), from: from}

	b.mu.Lock()

	if !b.take(from) {
		b.mu.Unlock()
		b.logf("refused %q from %s: %s\n", req.Who, from, busy)

		return Answer{Why: busy}
	}

	defer b.release(from)

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

	// a refusal says what it read, or nothing
	st := b.read(time.Now())
	ans := Answer{
		FreeMB: st.r.Free() - st.held, HeldMB: st.held, HeldCores: st.cores, FreeCores: b.freeCores(st.cores),
		Why: fmt.Sprintf("%d MB free less %d MB granted lately, pressure %.1f%%, for %d MB asked", st.r.Free(), st.held, st.pressure, req.MB),
	}

	if req.CPU {
		ans.Why += b.cpuSaid(st, req)
	}

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

	go b.probing(ctx)

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

// probing probes the tasks of the grants holding cores every ProbeEvery, until ctx ends.
func (b *Broker) probing(ctx context.Context) {
	if b.Gone == nil {
		return
	}

	tick := time.NewTicker(cmp.Or(b.ProbeEvery, 30*time.Second))
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			b.probe(ctx)
		}
	}
}

// probe asks whether the task of each grant holding cores is still there, and ends the cores of a
// grant whose task was gone on this probe and the one before.
func (b *Broker) probe(ctx context.Context) {
	now := time.Now()

	b.mu.Lock()

	var froms []string

	for _, g := range b.granted {
		if g.cores > 0 && g.from != "" && now.Before(g.coresUntil) {
			froms = append(froms, g.from)
		}
	}

	b.mu.Unlock()

	slices.Sort(froms)
	froms = slices.Compact(froms)

	// a gone task's address answers only once its probe times out, so they go at once
	gone := make([]bool, len(froms))

	var wg sync.WaitGroup

	for i, from := range froms {
		wg.Go(func() { gone[i] = b.Gone(ctx, from) })
	}

	wg.Wait()

	if ctx.Err() != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	freed := false

	for i := range b.granted {
		g := &b.granted[i]

		j, probed := slices.BinarySearch(froms, g.from)
		if !probed || g.cores <= 0 {
			continue
		}

		if !gone[j] {
			g.missed = 0

			continue
		}

		if g.missed++; g.missed >= 2 {
			g.cores, freed = 0, true

			_, cores := b.held(now)
			b.logf("released %q: its task at %s is gone, %.1f cores granted lately now\n", g.who, g.from, cores)
		}
	}

	if freed {
		b.poke()
	}
}

// TaskGone is whether nothing answers at the address from on the worker's network: a task's container
// that is there refuses a connection to a port nothing listens on at once, while a gone one's address
// answers nothing, or no route leads to it.
func TaskGone(ctx context.Context, from string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(from, "1"))
	if err == nil {
		_ = c.Close() //nolint:errcheck // only its answer counted

		return false
	}

	return !errors.Is(err, syscall.ECONNREFUSED)
}

// step grants asks in their order while the memory covers them; once the first has held the others back
// for FirstFor, the first after it that the memory covers goes ahead of it.
func (b *Broker) step() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		now := time.Now()
		st := b.read(now)

		i := b.next(st, now)
		if i < 0 {
			return
		}

		a := b.queue[i]
		b.queue = append(b.queue[:i], b.queue[i+1:]...)

		// a task's new grant replaces its last: one task holds one grant at most
		if a.from != "" {
			b.granted = slices.DeleteFunc(b.granted, func(g grant) bool { return g.from == a.from })
		}

		g := b.grantOf(a, now)
		b.granted = append(b.granted, g)

		ans := Answer{Granted: true, FreeMB: st.r.Free() - st.held, HeldMB: st.held + a.req.MB, HeldCores: st.cores + g.cores, FreeCores: b.freeCores(st.cores)}
		b.logf("granted %q %d MB, %.1f cores: %d MB free less %d MB granted lately, %.1f cores granted lately\n", a.req.Who, a.req.MB, g.cores, st.r.Free(), st.held, st.cores)

		a.done <- ans
	}
}

// grantOf is the grant of a, its memory counting for the ramp and its cores, when it waits for CPU, for
// its lease, else the ramp too.
func (b *Broker) grantOf(a *ask, now time.Time) grant {
	g := grant{mb: a.req.MB, until: now.Add(b.Ramp), from: a.from, who: a.req.Who}

	if a.req.CPU && a.req.Cores > 0 {
		lease := time.Duration(a.req.LeaseSeconds * float64(time.Second))
		if lease <= 0 {
			lease = b.Ramp
		}

		g.cores, g.coresUntil = a.req.Cores, now.Add(lease)
	}

	return g
}

// next is the queue's index of the ask to grant now, -1 for none.
func (b *Broker) next(st reading, now time.Time) int {
	for i, a := range b.queue {
		if memoryFits(a, st) && b.cpuFits(a, st) {
			return i
		}

		// the first in line holds the others back until FirstFor has passed
		if i == 0 && (b.FirstFor <= 0 || now.Sub(a.at) < b.FirstFor) {
			return -1
		}
	}

	return -1
}

func memoryFits(a *ask, st reading) bool {
	switch {
	case st.err != nil:
		// a worker whose memory can't be read grants as a batch without a broker starts: unchecked
		return true
	case a.req.MB > st.r.Capacity():
		// an ask no worker could ever cover starts at once, rather than wait for room never coming
		return true
	default:
		return st.r.Free()-st.held >= a.req.MB && st.pressure < admitPressure
	}
}

// cpuFits is whether an ask that waits for CPU may start: the CPU pressure, where it can be read, below
// the hold, and its cores beside those held within the worker's. An ask for more cores than the worker
// has starts at once, as one for more memory than it has does.
func (b *Broker) cpuFits(a *ask, st reading) bool {
	switch {
	case !a.req.CPU:
		return true
	case b.CPUHold > 0 && st.cpu >= b.CPUHold:
		return false
	default:
		return b.CPUs <= 0 || a.req.Cores <= 0 || a.req.Cores > b.CPUs || st.cores+a.req.Cores <= b.CPUs
	}
}

// drop ends the core lease of the grant of from, or of the grant named req.Who when from is none: the
// batch has ended, so its cores count no longer, while its memory counts until its ramp ends. It never
// waits.
func (b *Broker) drop(req Request, from string) Answer {
	b.init()

	b.mu.Lock()
	defer b.mu.Unlock()

	dropped := 0

	for i := range b.granted {
		g := &b.granted[i]
		if g.cores > 0 && (from != "" && g.from == from || from == "" && g.who == req.Who) {
			g.cores, dropped = 0, dropped+1
		}
	}

	_, cores := b.held(time.Now())
	b.poke()
	b.logf("released %q: the cores of %d grants, %.1f cores granted lately now\n", req.Who, dropped, cores)

	return Answer{Granted: true, HeldCores: cores, FreeCores: b.freeCores(cores)}
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
	if json.Unmarshal(line, &req) != nil || req.MB < 0 || req.WaitSeconds < 0 || req.WaitSeconds > maxWait.Seconds() || len(req.Who) > maxWho ||
		req.Cores < 0 || req.LeaseSeconds < 0 || req.LeaseSeconds > maxWait.Seconds() {
		answer(c, Answer{Why: "a request is one line of JSON: {\"mb\": <n>, \"wait_seconds\": <n>, \"who\": \"...\"}"})

		return
	}

	// a release never waits, and a task may send it while nothing of its waits
	if req.Release {
		answer(c, b.drop(req, source(c)))

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

	ans := b.ask(gone, req, source(c))

	answer(c, ans)
}

// source is the asking task's address; the worker's host itself, on loopback, is none: it may ask as often
// as it likes.
func source(c net.Conn) string {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return ""
	}

	if ip := net.ParseIP(host); ip == nil || ip.IsLoopback() {
		return ""
	}

	return host
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

// ErrNotReleased is a release the broker refused.
var ErrNotReleased = errors.New("the broker released nothing")

// Release tells the broker at addr that the batch who has ended, so its grant's cores count no longer.
func Release(ctx context.Context, addr, who string) error {
	ans, err := Ask(ctx, addr, Request{Release: true, Who: who})
	if err != nil {
		return err
	}

	if !ans.Granted {
		return fmt.Errorf("%w: %s", ErrNotReleased, ans.Why)
	}

	return nil
}

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
