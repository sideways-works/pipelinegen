// Package progress says on stderr, once a minute after the first, which stage a long step is in,
// how far it got and how long it has run, so a slow step can be told from a stuck one; at the end
// it can say where the step's time went.
package progress

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

var every = time.Minute

type Meter struct {
	mu     sync.Mutex
	prefix string
	out    io.Writer
	start  time.Time
	stage  string
	count  string
	// since is when the stage began; spent is the time each stage's key has had, git each network
	// command's count and time.
	since time.Time
	spent map[string]time.Duration
	git   map[string]*calls
}

type calls struct {
	n int
	d time.Duration
}

type key struct{}

// Start begins saying where the step is on out, each line after prefix; the context carries the
// meter for Stage and Count, and stop ends it.
func Start(ctx context.Context, out io.Writer, prefix string) (context.Context, func()) {
	now := time.Now()
	m := &Meter{prefix: prefix, out: out, start: now, since: now, spent: map[string]time.Duration{}, git: map[string]*calls{}}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		tick := time.NewTicker(every)
		defer tick.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.say()
			}
		}
	}()

	return context.WithValue(ctx, key{}, m), func() {
		cancel()
		<-done
	}
}

func from(ctx context.Context) *Meter {
	m, _ := ctx.Value(key{}).(*Meter) //nolint:errcheck // a context without a meter says nothing
	return m
}

// Stage names the stage the step is in now and returns what restores the one before, with its
// count, for a stage within another: defer it.
func Stage(ctx context.Context, format string, args ...any) func() {
	m := from(ctx)
	if m == nil {
		return func() {}
	}

	m.mu.Lock()
	stage, count := m.stage, m.count
	m.charge(time.Now())
	m.stage = fmt.Sprintf(format, args...)
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		m.charge(time.Now())
		m.stage, m.count = stage, count
		m.mu.Unlock()
	}
}

// charge gives the time since the stage began to the stage's key; the caller holds mu.
func (m *Meter) charge(now time.Time) {
	m.spent[stageKey(m.stage)] += now.Sub(m.since)
	m.since = now
}

// stageKey is what a stage's time is summed under: its words before a colon, so a plugin's calls add up
// under the plugin, and "other" for the time outside every stage.
func stageKey(stage string) string {
	if stage == "" {
		return "other"
	}

	key, _, _ := strings.Cut(stage, ": ")

	return key
}

// Git counts a git command that reaches a remote (fetch, ls-remote, push, clone) and its time: call it
// with git's arguments before the command runs and call what it returns once it ended. Any other
// command is not counted.
func Git(ctx context.Context, args []string) func() {
	m := from(ctx)
	verb := gitVerb(args)

	if m == nil || !slices.Contains([]string{"fetch", "ls-remote", "push", "clone"}, verb) {
		return func() {}
	}

	began := time.Now()

	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		c := m.git[verb]
		if c == nil {
			c = &calls{}
			m.git[verb] = c
		}

		c.n++
		c.d += time.Since(began)
	}
}

// gitVerb is git's command among its arguments: the first that is no option, skipping the values of
// -c and -C.
func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case !strings.HasPrefix(a, "-"):
			return a
		}
	}

	return ""
}

// Summary says where the step's time went: its total, the time of each stage's key, the longest first,
// and the git commands that reached a remote, with their time. Keys under a tenth of a second are left
// out.
func Summary(ctx context.Context) string {
	m := from(ctx)
	if m == nil {
		return ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.charge(now)

	keys := make([]string, 0, len(m.spent))
	for k, d := range m.spent {
		if d >= 100*time.Millisecond {
			keys = append(keys, k)
		}
	}

	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(m.spent[b], m.spent[a]), cmp.Compare(a, b))
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+seconds(m.spent[k]))
	}

	line := m.prefix + ": took " + seconds(now.Sub(m.start))
	if len(parts) > 0 {
		line += ": " + strings.Join(parts, ", ")
	}

	verbs := make([]string, 0, len(m.git))
	total, spent := 0, time.Duration(0)

	for v, c := range m.git {
		verbs = append(verbs, v)
		total += c.n
		spent += c.d
	}

	slices.Sort(verbs)

	each := make([]string, 0, len(verbs))
	for _, v := range verbs {
		each = append(each, fmt.Sprintf("%d %s", m.git[v].n, v))
	}

	line += fmt.Sprintf("; git reached a remote %d times in %s", total, seconds(spent))
	if total > 0 {
		line += " (" + strings.Join(each, ", ") + ")"
	}

	return line + "\n"
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// Count says how far the step got, where it can tell: done of total things ("2 of 5 plugins"); a
// total of 0 clears it.
func Count(ctx context.Context, done, total int, things string) {
	if m := from(ctx); m != nil {
		m.mu.Lock()

		m.count = ""
		if total > 0 {
			m.count = fmt.Sprintf("%d of %d %s", done, total, things)
		}

		m.mu.Unlock()
	}
}

func (m *Meter) say() {
	_, _ = io.WriteString(m.out, m.line()) //nolint:errcheck // a line that can't be said changes nothing
}

func (m *Meter) line() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	line := m.prefix + ": "
	if m.stage != "" {
		line += m.stage + ", "
	}

	if m.count != "" {
		line += m.count + " done, "
	}

	return line + fmt.Sprintf("%s so far\n", time.Since(m.start).Round(time.Second))
}
