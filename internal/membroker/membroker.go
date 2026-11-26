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
// The pipeline's own steps (a plan, a suite job's bookkeeping, a suite's prepare or whole-suite check)
// ask as batches do, marked as steps: such an ask goes ahead of the first in line when the worker has
// room for it, and one whose wait ends unanswered is granted then, counted and leased as any grant, since
// the step starts anyway.
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
	"io/fs"
	"net"
	"os"
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
	// Step marks the ask of a step of the pipeline's own, not a batch: a plan, a suite job's bookkeeping,
	// a suite's prepare or whole-suite check. It goes ahead of the first in line whenever the worker has
	// room for it, since its own wait is short and the batches wait on it, and its grant starts no settle
	// time for later asks for cores.
	Step bool `json:"step,omitempty"`
	// Anyway grants the ask when its wait ends unanswered, its memory and cores counted and leased as any
	// grant's: its task starts then whatever the worker has, and the asks after it see what it holds.
	Anyway bool `json:"anyway,omitempty"`
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
	// Anyway says the grant came when the ask's wait ended, the worker short of what it asked; Why then
	// says what the broker read.
	Anyway bool `json:"anyway,omitempty"`
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
	// Past it the first of the batches' asks for cores keeps its cores: the asks after it are granted
	// only while their cores fit beside its own and those held.
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
	// CPUSettle is how long after a grant of cores the next ask for cores waits, so the pressure it
	// reads shows the batch granted before it: grants read the same pressure until the batches they
	// started run, and asks granted at once would start together on a busy worker. 0 settles for none.
	CPUSettle time.Duration
	// Gone says whether the task at a grant's address has ended. A grant whose task is gone on two
	// probes in a row holds its cores no longer, its memory counting until its ramp ends. nil probes
	// none, and a grant's cores count until its release or its lease's end.
	Gone func(ctx context.Context, from string) bool
	// ProbeEvery is how often the tasks of the grants holding cores are probed, 30 seconds when 0.
	ProbeEvery time.Duration
	// State is the file the broker keeps its grants in, so a restart forgets none: the batches it granted
	// run on, and a broker that started empty would grant their memory and cores again beside them. ""
	// keeps none.
	State string

	mu      sync.Mutex
	pending map[string]bool
	queue   []*ask
	granted []grant
	wake    chan struct{}
	conns   chan struct{}
	once    sync.Once
	// coresAt is when cores were last granted.
	coresAt time.Time
}

type ask struct {
	req  Request
	done chan Answer
	at   time.Time
	// from is the asking task's address: one ask at a time from it, and its grant replaces its last.
	from string
}

// kept is a grant as State keeps it.
type kept struct {
	MB         int64     `json:"mb"`
	Until      time.Time `json:"until"`
	From       string    `json:"from,omitempty"`
	Who        string    `json:"who"`
	Cores      float64   `json:"cores,omitempty"`
	CoresUntil time.Time `json:"cores_until,omitzero"`
}

// save writes the grants to State; b.mu is held. A grant that can't be kept still counts, until a restart.
func (b *Broker) save() {
	if b.State == "" {
		return
	}

	out := make([]kept, 0, len(b.granted))
	for _, g := range b.granted {
		out = append(out, kept{MB: g.mb, Until: g.until, From: g.from, Who: g.who, Cores: g.cores, CoresUntil: g.coresUntil})
	}

	data, err := json.Marshal(out)

	tmp := b.State + ".new"
	if err == nil {
		err = os.WriteFile(tmp, data, 0o600)
	}

	if err == nil {
		err = os.Rename(tmp, b.State)
	}

	if err != nil {
		b.logf("keeping the grants: %v\n", err)
	}
}

// load takes the grants State kept that still count at now, and logs what they hold; b.mu is held.
func (b *Broker) load(now time.Time) {
	if b.State == "" {
		return
	}

	data, err := os.ReadFile(b.State)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}

	var saved []kept
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}

	if err != nil {
		b.logf("the grants kept: %v; the broker starts with none\n", err)

		return
	}

	for _, k := range saved {
		b.granted = append(b.granted, grant{mb: k.MB, until: k.Until, from: k.From, who: k.Who, cores: k.Cores, coresUntil: k.CoresUntil})
	}

	mb, cores := b.held(now)
	b.logf("kept %d grants from before the start: %d MB and %.1f cores granted lately\n", len(b.granted), mb, cores)
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

// cpuSaid is what the broker read of the CPU, for a refusal of an ask that waits for it, and the cores an
// ask before it keeps.
func (b *Broker) cpuSaid(st reading, a *ask, now time.Time) string {
	said := fmt.Sprintf("; %.1f of %.1f cores granted lately, CPU pressure %s, for %.1f cores asked", st.cores, b.CPUs, cpuPSI(st.cpu), a.req.Cores)

	if keep := b.keeping(now); a.req.Cores > 0 && b.kept(a, keep) > 0 {
		said += fmt.Sprintf(", %.1f kept for %q, waiting %s", b.kept(a, keep), keep.req.Who, now.Sub(keep.at).Round(time.Second))
	}

	return said
}

// cpuPSI says a CPU pressure read, "unknown" where it couldn't be.
func cpuPSI(cpu float64) string {
	if cpu < 0 {
		return "unknown"
	}

	return fmt.Sprintf("%.1f%%", cpu)
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

	// a wait that ended grants an ask that starts anyway; a task gone takes nothing
	anyway := false

	select {
	case ans := <-a.done:
		return ans
	case <-timer.C:
		// a task that went as its wait ended starts nothing
		anyway = req.Anyway && ctx.Err() == nil
	case <-ctx.Done():
	}

	return b.ended(a, wait, anyway)
}

// ended answers a whose wait ended, or whose task went: its grant if it came meanwhile, else its grant
// anyway when it starts anyway, else its refusal.
func (b *Broker) ended(a *ask, wait time.Duration, anyway bool) Answer {
	req := a.req

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

	// a refusal, or a grant anyway, says what it read
	now := time.Now()
	st := b.read(now)
	why := fmt.Sprintf("%d MB free less %d MB granted lately, pressure %.1f%%, for %d MB asked", st.r.Free(), st.held, st.pressure, req.MB)

	if req.CPU {
		why += b.cpuSaid(st, a, now)
	}

	if anyway {
		ans := b.grant(a, st, now, "granted anyway after "+wait.Round(time.Second).String())
		ans.Anyway, ans.Why = true, why

		return ans
	}

	ans := Answer{FreeMB: st.r.Free() - st.held, HeldMB: st.held, HeldCores: st.cores, FreeCores: b.freeCores(st.cores), Why: why}

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

	var freed []grant

	// held drops ended grants from b.granted, so it runs only once this loop is done with it
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
			freed = append(freed, *g)
			g.cores = 0
		}
	}

	if len(freed) == 0 {
		return
	}

	_, cores := b.held(now)
	b.save()

	for _, g := range freed {
		b.logf("released %q: its task at %s is gone, %.1f cores granted lately now\n", g.who, g.from, cores)
	}

	b.poke()
}

// TaskGone is whether the task at the address from is gone from the worker's network: no host answers
// for its address, so no route leads to it. A task's container that is there refuses a connection to a
// port nothing listens on at once. A connection that times out says neither: a firewall may drop the
// refusal, so the task counts as there, and its cores count until its release or its lease's end.
func TaskGone(ctx context.Context, from string) bool {
	// the kernel gives up on an address no host answers for after about three seconds
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(from, "1"))
	if err == nil {
		_ = c.Close() //nolint:errcheck // only its answer counted
	}

	return goneBy(err)
}

// taskAt names the asking task's address for the log, which the probes and a release go by.
func taskAt(from string) string {
	if from == "" {
		return ""
	}

	return "; task at " + from
}

// goneBy is whether a probe's error says no host has the address.
func goneBy(err error) bool {
	return errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH)
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

		a.done <- b.grant(a, st, now, "granted")
	}
}

// grant records a's grant, read st at now, and logs it as what: granted, or granted anyway once its
// wait ended; b.mu is held.
func (b *Broker) grant(a *ask, st reading, now time.Time, what string) Answer {
	// a task's new grant replaces its last: one task holds one grant at most
	if a.from != "" {
		b.granted = slices.DeleteFunc(b.granted, func(g grant) bool { return g.from == a.from })
	}

	g := b.grantOf(a, now)
	b.granted = append(b.granted, g)
	b.save()

	// the settle time spaces batches' grants, so the pressure each reads shows the batch before it; a
	// step's grant starts none, and a batch after it reads the pressure as it finds it
	if g.cores > 0 && !a.req.Step {
		b.coresAt = now
	}

	b.logf("%s %q %d MB, %.1f cores: %d MB free less %d MB granted lately, pressure %.1f%%, %.1f cores granted lately, CPU pressure %s%s\n",
		what, a.req.Who, a.req.MB, g.cores, st.r.Free(), st.held, st.pressure, st.cores, cpuPSI(st.cpu), taskAt(a.from))

	return Answer{Granted: true, FreeMB: st.r.Free() - st.held, HeldMB: st.held + a.req.MB, HeldCores: st.cores + g.cores, FreeCores: b.freeCores(st.cores)}
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

		// an ask for more than all the cores starts at once, and holds them all: no more
		g.cores, g.coresUntil = a.req.Cores, now.Add(lease)
		if b.CPUs > 0 {
			g.cores = min(g.cores, b.CPUs)
		}
	}

	return g
}

// next is the queue's index of the ask to grant now, -1 for none.
func (b *Broker) next(st reading, now time.Time) int {
	held := false
	keep := b.keeping(now)

	for i, a := range b.queue {
		if (!held || a.req.Step) && memoryFits(a, st) && b.cpuFits(a, st, keep) && !b.settling(a, now) {
			return i
		}

		// the first in line holds the others back until FirstFor has passed, all but a step's ask
		if i == 0 && (b.FirstFor <= 0 || now.Sub(a.at) < b.FirstFor) {
			held = true
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

// settling is whether an ask for cores waits for the last grant of cores to show in the pressure.
func (b *Broker) settling(a *ask, now time.Time) bool {
	return a.req.CPU && a.req.Cores > 0 && b.CPUSettle > 0 && !b.coresAt.IsZero() && now.Sub(b.coresAt) < b.CPUSettle
}

// keeping is the ask for cores that keeps them from the batches' asks after it: the first in line of the
// batches' asks for cores, once it has waited FirstFor. Past that the asks after it the worker has room
// for go ahead of it, and on a worker whose cores free a few at a time the smaller asks would take each
// core that frees, so an ask for many waited until its wait ended; their cores now fit beside its own, and
// it starts once enough of the cores held have ended. nil for none; b.mu is held.
func (b *Broker) keeping(now time.Time) *ask {
	if b.CPUs <= 0 || b.FirstFor <= 0 {
		return nil
	}

	for _, a := range b.queue {
		if a.req.CPU && a.req.Cores > 0 && !a.req.Step {
			if now.Sub(a.at) < b.FirstFor {
				return nil
			}

			return a
		}
	}

	return nil
}

// kept is the cores keep keeps from a, 0 when keep is none or a itself, or a is a step's ask, which goes
// ahead whenever the worker has room for it.
func (b *Broker) kept(a, keep *ask) float64 {
	if keep == nil || keep == a || a.req.Step {
		return 0
	}

	return min(keep.req.Cores, b.CPUs)
}

// cpuFits is whether an ask that waits for CPU may start: the CPU pressure, where it can be read, below
// the hold, and its cores beside those held, and those keep keeps, within the worker's. An ask for more
// cores than the worker has starts at once, as one for more memory than it has does, unless an ask
// before it keeps cores.
func (b *Broker) cpuFits(a *ask, st reading, keep *ask) bool {
	switch {
	case !a.req.CPU:
		return true
	case b.CPUHold > 0 && st.cpu >= b.CPUHold:
		return false
	case b.CPUs > 0 && a.req.Cores > 0 && b.kept(a, keep) > 0:
		return st.cores+min(a.req.Cores, b.CPUs)+b.kept(a, keep) <= b.CPUs
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
	b.save()
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
	// the grants kept from before a restart count before the first ask is read
	b.mu.Lock()
	b.load(time.Now())
	b.mu.Unlock()

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

// errBroke is a broker that took the ask and went away before it answered: it restarted.
var errBroke = errors.New("the broker went away before it answered")

const (
	// redialFor is how long an ask keeps dialing a broker it can't reach: a broker that restarts is back
	// within it, and one that isn't there leaves the asker to its own reading after it.
	redialFor = 30 * time.Second
	// askAgainAfter is the pause before asking again a broker that couldn't be reached or went away.
	askAgainAfter = 3 * time.Second
)

// AskAgain asks as Ask does, and asks again when the broker goes away before it answers (a restart forgets
// every ask, and an asker that then went by its own reading would start beside every other one at once),
// within what is left of req's wait; it keeps dialing a broker it can't reach for redialFor. It ends with
// ErrUnreachable when the broker can't be reached by then, or its wait ends without an answer.
func AskAgain(ctx context.Context, addr string, req Request) (Answer, error) {
	start := time.Now()
	deadline := start.Add(time.Duration(req.WaitSeconds * float64(time.Second)))

	for {
		ans, err := Ask(ctx, addr, req)
		if err == nil || ctx.Err() != nil || !errors.Is(err, ErrUnreachable) {
			return ans, err
		}

		left := time.Until(deadline)
		if left <= askAgainAfter || (!errors.Is(err, errBroke) && time.Since(start) >= redialFor) {
			return ans, err
		}

		select {
		case <-ctx.Done():
			return Answer{}, fmt.Errorf("membroker: %w", ctx.Err())
		case <-time.After(askAgainAfter):
		}

		req.WaitSeconds = time.Until(deadline).Seconds()
	}
}

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
		return Answer{}, fmt.Errorf("%w: %s: %w: %w", ErrUnreachable, addr, errBroke, err)
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

		return Answer{}, fmt.Errorf("%w: %s: %w: %w", ErrUnreachable, addr, errBroke, err)
	}

	return ans, nil
}
