// Package tries decides whether a failed write goes again and after what wait: a passing failure
// with growing waits until a deadline, a remote's rule a few times, a defect never.
package tries

import (
	"context"
	"fmt"
	mrand "math/rand/v2"
	"regexp"
	"strings"
	"time"
)

type Kind int

const (
	// Passing is a failure another try may not meet: a dropped connection, a host busy with other
	// pushes.
	Passing Kind = iota
	// Ruled is a refusal by a rule of the remote's, a hook's or a permission's: retried at most
	// RuledRetries times, since a host under load declines the same way now and then.
	Ruled
	// Defect is a failure trying again repeats.
	Defect
)

const RuledRetries = 2

const MaxWait = 30 * time.Second

type Policy struct {
	// Retries is how often a passing failure goes again at least, however long that takes within
	// Until; 0 tries once, whatever the deadline.
	Retries int
	// First is the first retry's wait; each later one doubles it up to MaxWait, cut to a random
	// length between half and all of it, so writers that met don't meet again in step.
	First time.Duration
	// Deadline is how long after the first try a passing failure keeps going once Retries are
	// spent; zero, Retries alone bound it.
	Deadline time.Duration
	// Until is the hard end: no wait goes past it, nor past the context's deadline, the step's
	// time limit.
	Until time.Time
}

type Run struct {
	p              Policy
	start          time.Time
	retries, ruled int
	stop           string

	now   func() time.Time
	sleep func(context.Context, time.Duration)
}

func (p Policy) Start() *Run {
	return &Run{p: p, start: time.Now(), now: time.Now, sleep: sleep}
}

// Again tells whether the write goes again after a failure of kind k, having waited when it does;
// no once ctx is done.
func (r *Run) Again(ctx context.Context, k Kind) bool {
	if ctx.Err() != nil {
		r.stop = "its context ended"

		return false
	}

	switch k {
	case Defect:
		r.stop = "trying again repeats it"

		return false
	case Ruled:
		if r.ruled >= min(r.p.Retries, RuledRetries) {
			r.stop = "the remote refuses it by a rule of its own"

			return false
		}

		r.ruled++
	case Passing:
		if r.retries >= r.p.Retries && (r.p.Retries == 0 || r.now().Sub(r.start)+r.wait() > r.p.Deadline) {
			r.stop = r.spent()

			return false
		}
	}

	w := r.jittered()
	if end := HardEnd(ctx, r.p.Until); !end.IsZero() && r.now().Add(w).After(end) {
		r.stop = stopLimit

		return false
	}

	r.retries++
	r.sleep(ctx, w)

	return ctx.Err() == nil
}

const stopLimit = "the step's time limit is near"

// HardEnd is the earlier of until and ctx's deadline (the step's time limit); zero for neither.
func HardEnd(ctx context.Context, until time.Time) time.Time {
	if dl, ok := ctx.Deadline(); ok && (until.IsZero() || dl.Before(until)) {
		return dl
	}

	return until
}

func (r *Run) spent() string {
	if r.p.Retries == 0 {
		return "no retries are set"
	}

	if r.p.Deadline == 0 {
		return fmt.Sprintf("its %d retries are spent", r.p.Retries)
	}

	return fmt.Sprintf("its deadline of %s is near", r.p.Deadline)
}

func (r *Run) Retries() int { return r.retries }

func (r *Run) wait() time.Duration {
	if r.p.First <= 0 {
		return 0
	}

	w := r.p.First
	for range r.retries {
		if w >= MaxWait {
			break
		}

		w *= 2
	}

	return min(w, max(MaxWait, r.p.First))
}

func (r *Run) jittered() time.Duration {
	w := r.wait()
	if w <= 1 {
		return w
	}

	return w/2 + time.Duration(mrand.Int64N(int64(w-w/2))) //nolint:gosec // jitter, not a secret
}

// Err is the write's last failure with how many tries, how long, and why it stopped; a write that
// never went again is said as it failed, unless its step's time limit stopped it.
func (r *Run) Err(err error) error {
	if err == nil || r.retries == 0 && r.stop != stopLimit {
		return err
	}

	why := ""
	if r.stop != "" {
		why = ", stopped as " + r.stop
	}

	tries := "1 try"
	if r.retries > 0 {
		tries = fmt.Sprintf("%d tries", r.retries+1)
	}

	return fmt.Errorf("%s over %s%s; the last: %w", tries, r.now().Sub(r.start).Round(time.Second), why, err)
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

var ruledWords = []string{
	"hook declined", "denied", "not allowed", "protected branch", "Forbidden", "error: 403",
	"Authentication failed", "Repository not found", "does not appear to be a git repository",
	"could not read Username", "terminal prompts disabled",
}

// named are the parts of git's words that name things rather than say what happened: refs and
// quoted URLs, which could hold any word.
var named = regexp.MustCompile(`refs/\S+|'[^']*'`)

// Of tells the kind of a git command's failure from what git printed: a refusal by a rule, or
// else a passing failure.
func Of(printed string) Kind {
	printed = named.ReplaceAllString(printed, "")

	for _, w := range ruledWords {
		if strings.Contains(printed, w) {
			return Ruled
		}
	}

	return Passing
}
