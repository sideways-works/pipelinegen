package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

// A continuous run runs its rounds back to back, each of the branch's newest commit. The deciding
// build reads whether a round is in progress from the run's own records; it never takes or holds anything.
const (
	supersededPrefix = "refs/pipelinegen/superseded/"
	supersededFile   = "superseded.json"
)

var (
	roundPoll = 15 * time.Second
	roundUnit = time.Minute
)

// pastBound rounds up to whole seconds: a round is dead once its silence exceeds the bound, so the time
// said is always more than the bound.
func pastBound(quiet time.Duration) time.Duration {
	if s := quiet.Truncate(time.Second); s < quiet {
		return s + time.Second
	}

	return quiet
}

// roundStep lets a test pause one build between two steps of its decision.
var roundStep = func(_, _ string) {}

const roundSayEvery = time.Minute

func isContinuous(cfg *config.Config, run string) bool {
	r, ok := cfg.Runs[run]

	return ok && r.Trigger.Continuous
}

func supersededRef(run, commit string) string {
	return supersededPrefix + run + "/" + short12(commit)
}

type superseded struct {
	Change string    `json:"change"`
	Commit string    `json:"commit"`
	Tip    string    `json:"tip"`
	At     time.Time `json:"at"`
	JobURL string    `json:"job_url,omitempty"`
}

var errRoundGoesOn = errors.New("the round in progress didn't end")

type round struct {
	commit string
	last   time.Time
	ended  bool
}

// newestRound reads only the suites the run covers: a kept plan may hold others the run has no job for.
func newestRound(ctx context.Context, store *gitstore.Store, cfg *config.Config, ch runs.Change) (round, bool, error) {
	plans, err := store.List(ctx, report.PlansPrefix(ch.Run, ch.ID))
	if err != nil {
		return round{}, false, err
	}

	var (
		newest gitstore.Ref
		name   string
	)

	for n, ref := range plans {
		if ref.Time.After(newest.Time) || name == "" {
			newest, name = ref, n
		}
	}

	if name == "" {
		return round{}, false, nil
	}

	id := report.ID{Run: ch.Run, Change: ch.ID, Commit: name[strings.LastIndex(name, "/")+1:]}
	r := round{commit: id.Commit, last: newest.Time}

	plan, ok, err := readPlan(ctx, store, id)
	if err != nil || !ok {
		return r, ok, err
	}

	refs, err := store.List(ctx, id.Prefix())
	if err != nil {
		return round{}, false, err
	}

	for _, ref := range refs {
		if ref.Time.After(r.last) {
			r.last = ref.Time
		}
	}

	// a round ended by hand is over, whatever its suites recorded.
	_, r.ended = refs[endedRef(id)]
	if r.ended {
		return r, true, nil
	}

	r.ended = true

	for _, sp := range plan.Suites {
		if _, ok := refs[id.EndRef(sp.Suite)]; !ok && cfg.RunCovers(ch.Run, sp.Suite) {
			r.ended = false
		}
	}

	return r, true, nil
}

// awaitRound runs the build once its commit is the branch's tip and no round is in progress, for at most
// wait_minutes; each build records its round and rereads the tip, so of two at once the older exits.
func awaitRound(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string, stdout io.Writer) (bool, error) {
	run, _, err := decideRound(ctx, cfg, ch, store, trigger, stdout, nil)

	return run, err
}

// roundDecision carries a decision made before the prepare to the plan step, which then records no
// second start and writes its planning over that version.
type roundDecision struct {
	Commit   string `json:"commit"`
	Planning string `json:"planning,omitempty"`
}

const decidedFile = "decided.json"

func decideRound(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string, stdout io.Writer, prior *roundDecision) (bool, roundDecision, error) {
	d := decider{
		cfg: cfg, ch: ch, store: store, trigger: trigger, stdout: stdout,
		branch:   cfg.Runs[ch.Run].Trigger.Branch,
		auth:     readAuth(ctx, cfg, trigger),
		idle:     time.Duration(cfg.IdleLength(ch.Run)) * roundUnit,
		planning: time.Duration(cfg.PlanningLength()) * roundUnit,
		dead:     map[string]bool{},
	}

	if prior != nil && prior.Commit == ch.Commit {
		d.started, d.planned = true, prior.Planning
	}

	run, err := d.await(ctx)

	return run, roundDecision{Commit: ch.Commit, Planning: d.planned}, err
}

// Deciding before the prepare means what the prepare leaves (an expiring credential cache) lives from
// the end of the wait, and a build that won't plan never prepares.
func decideBeforePrepare(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, gate string, stdout io.Writer) error {
	if !isContinuous(cfg, ch.Run) {
		return fmt.Errorf("%w: --decide is for a continuous run, and %s is not one", errUsage, ch.Run)
	}

	if done, err := ranAlready(ctx, cfg, ch, store, stdout); done || err != nil {
		if err != nil {
			return err
		}

		return writeHandOffGate(gate, ch, false)
	}

	run, dec, err := decideRound(ctx, cfg, ch, store, trigger, stdout, nil)
	if err != nil {
		return err
	}

	if !run {
		return writeHandOffGate(gate, ch, false)
	}

	data, err := json.Marshal(dec)
	if err != nil {
		return fmt.Errorf("decided: %w", err)
	}

	if err := os.MkdirAll(gate, 0o750); err != nil {
		return fmt.Errorf("decided: %w", err)
	}

	if err := os.WriteFile(filepath.Join(gate, decidedFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("decided: %w", err)
	}

	return nil
}

func readDecided(gate string) (*roundDecision, error) {
	data, err := os.ReadFile(filepath.Join(gate, decidedFile)) //nolint:gosec // the build's own hand-off directory
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil //nolint:nilnil // no earlier decision
	}

	if err != nil {
		return nil, fmt.Errorf("decided: %w", err)
	}

	var d roundDecision
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("decided: %w", err)
	}

	return &d, nil
}

type decider struct {
	cfg     *config.Config
	ch      runs.Change
	store   *gitstore.Store
	trigger string
	stdout  io.Writer
	branch  string
	auth    runs.Auth

	// idle bounds a planned round's silence, planning an unplanned one's.
	idle, planning time.Duration

	planned string
	// fresh: the planning record was just written; the build plans only once a reading after it still
	// finds no round in progress, so of two builds deciding at once each sees the other
	fresh   bool
	dead    map[string]bool
	started bool

	said   string
	saidAt time.Time
}

type busy struct {
	commit      string
	quiet, dead time.Duration
	unplanned   bool
}

func (d *decider) await(ctx context.Context) (bool, error) {
	start := time.Now()
	wait := time.Duration(d.cfg.Runs[d.ch.Run].WaitLength()) * roundUnit

	for first := true; ; first = false {
		if newest, err := d.newest(ctx); err != nil || !newest {
			return false, err
		}

		if first && !d.started {
			if run, err := d.start(ctx); err != nil || !run {
				return false, err
			}

			continue
		}

		b, err := d.inProgress(ctx)
		if err != nil {
			return false, err
		}

		if b.commit == "" {
			again, run, err := d.plans(ctx)
			if again {
				continue
			}

			return run, err
		}

		d.fresh = false

		if time.Since(start) > wait {
			return false, fmt.Errorf("continuous run %s: %w: waited %s for the round of %s (its last step was %s ago): raise runs.%s.wait_minutes, or end a round whose build is gone with pipelinegen end --run %s --commit %s",
				d.ch.Run, errRoundGoesOn, wait, b.commit, b.quiet.Round(time.Second), d.ch.Run, d.ch.Run, b.commit)
		}

		if err := d.sayWaiting(b); err != nil {
			return false, err
		}

		t := time.NewTimer(roundPoll)

		select {
		case <-ctx.Done():
			t.Stop()

			return false, fmt.Errorf("continuous run %s: waiting for the round of %s: %w", d.ch.Run, b.commit, ctx.Err())
		case <-t.C:
		}
	}
}

func (d *decider) plans(ctx context.Context) (again, run bool, err error) {
	if d.fresh {
		return false, true, say(d.stdout, "%s %s: the newest of %s, and no round in progress: planning\n", d.ch.Run, short12(d.ch.Commit), d.branch)
	}

	d.planned, err = report.RecordRound(ctx, d.store, reportID(d.ch), report.RoundPlanning, os.Getenv(envJobURL), d.planned)

	switch {
	case errors.Is(err, report.ErrRoundRecorded):
		return false, false, say(d.stdout, "%s %s: nothing changed since the %s of %s: not run\n", d.ch.Run, short12(d.ch.Commit), d.ch.Run, short12(d.ch.Commit))
	case err != nil:
		return false, false, err
	}

	d.fresh = true

	roundStep(d.ch.Commit, "recorded")

	return true, false, nil
}

func (d *decider) newest(ctx context.Context) (bool, error) {
	tip, err := runs.FetchTip(ctx, d.trigger, d.branch, d.auth)
	if err != nil {
		return false, fmt.Errorf("continuous run %s: the tip of %s: %w", d.ch.Run, d.branch, err)
	}

	if tip != d.ch.Commit {
		return false, supersede(ctx, d.ch, d.store, tip, d.stdout)
	}

	roundStep(d.ch.Commit, "looked")

	return true, nil
}

// start records that the build's round started. A build of the same commit that recorded it first has
// the round; one that died before its plan, older than limits.plan, is started over.
func (d *decider) start(ctx context.Context) (bool, error) {
	id := reportID(d.ch)

	_, err := report.RecordRound(ctx, d.store, id, report.RoundStarted, os.Getenv(envJobURL), "")
	if !errors.Is(err, report.ErrRoundRecorded) {
		if err == nil {
			roundStep(d.ch.Commit, "recorded")
		}

		return err == nil, err
	}

	rounds, err := report.Rounds(ctx, d.store, d.ch.Run, d.ch.ID)
	if err != nil {
		return false, err
	}

	own := rounds[short12(d.ch.Commit)]

	if quiet := time.Since(own.Last()); quiet <= d.planning {
		return false, say(d.stdout, "%s %s: nothing changed since the %s of %s: not run\n", d.ch.Run, short12(d.ch.Commit), d.ch.Run, short12(d.ch.Commit))
	}

	if _, err := report.RecordRound(ctx, d.store, id, report.RoundStarted, os.Getenv(envJobURL), own.Records[report.RoundStarted].Commit); err != nil {
		return false, err
	}

	d.planned = own.Records[report.RoundPlanning].Commit

	return true, say(d.stdout, "%s %s: an earlier build of %s recorded nothing for %s and wrote no plan: started again\n",
		d.ch.Run, short12(d.ch.Commit), short12(d.ch.Commit), time.Since(own.Last()).Round(time.Second))
}

func (d *decider) inProgress(ctx context.Context) (busy, error) {
	r, ok, err := newestRound(ctx, d.store, d.cfg, d.ch)
	if err != nil {
		return busy{}, err
	}

	quiet := time.Since(r.last)

	if ok && !r.ended && !strings.HasPrefix(d.ch.Commit, r.commit) {
		if quiet <= d.idle {
			return busy{commit: short12(r.commit), quiet: quiet, dead: d.idle}, nil
		}

		if err := d.sayDead(r.commit, quiet, d.idle, "has recorded nothing for"); err != nil {
			return busy{}, err
		}
	}

	return d.unplanned(ctx, r.last)
}

// Rounds recorded before the newest plan were followed by a later one; only one recorded since is said dead.
func (d *decider) unplanned(ctx context.Context, newestPlan time.Time) (busy, error) {
	rounds, err := report.Rounds(ctx, d.store, d.ch.Run, d.ch.ID)
	if err != nil {
		return busy{}, err
	}

	plans, err := d.store.List(ctx, report.PlansPrefix(d.ch.Run, d.ch.ID))
	if err != nil {
		return busy{}, err
	}

	gone, err := d.store.List(ctx, supersededPrefix+d.ch.Run+"/")
	if err != nil {
		return busy{}, err
	}

	var oldest busy

	for commit, r := range rounds {
		id := report.ID{Run: d.ch.Run, Change: d.ch.ID, Commit: commit}
		if _, planned := plans[id.PlanRef()]; planned || strings.HasPrefix(d.ch.Commit, commit) {
			continue
		}

		if _, superseded := gone[supersededRef(d.ch.Run, commit)]; superseded {
			continue
		}

		// a round whose plan failed is over, unless a build of it recorded something since
		if failed, ok := r.Records[report.RoundFailed]; ok && !r.Last().After(failed.Time) {
			continue
		}

		quiet := time.Since(r.Last())
		if quiet > d.planning {
			if r.Last().After(newestPlan) {
				if err := d.sayDead(commit, quiet, d.planning, "has written no plan and recorded nothing for"); err != nil {
					return busy{}, err
				}
			}

			continue
		}

		if ended, err := endedByHand(ctx, d.store, id); err != nil {
			return busy{}, err
		} else if ended {
			continue
		}

		if oldest.commit == "" || quiet > oldest.quiet {
			oldest = busy{commit: commit, quiet: quiet, dead: d.planning, unplanned: true}
		}
	}

	return oldest, nil
}

func (d *decider) sayDead(commit string, quiet, bound time.Duration, what string) error {
	if d.dead[short12(commit)] {
		return nil
	}

	d.dead[short12(commit)] = true

	return say(d.stdout, "%s %s: the round of %s %s %s, more than %s: it counts as dead\n",
		d.ch.Run, short12(d.ch.Commit), short12(commit), what, pastBound(quiet), bound)
}

func (d *decider) sayWaiting(b busy) error {
	if b.commit == d.said && time.Since(d.saidAt) < roundSayEvery {
		return nil
	}

	d.said, d.saidAt = b.commit, time.Now()

	if b.unplanned {
		return say(d.stdout, "%s %s: waiting for the round of %s to end (it is deciding or planning; its last step was %d seconds ago; it counts as dead after %s, limits.plan and a margin)\n",
			d.ch.Run, short12(d.ch.Commit), b.commit, int(b.quiet.Seconds()), b.dead)
	}

	return say(d.stdout, "%s %s: waiting for the round of %s to end (its last step was %d seconds ago; it counts as dead after %s)\n",
		d.ch.Run, short12(d.ch.Commit), b.commit, int(b.quiet.Seconds()), b.dead)
}

// planWithin bounds a recorded round's planning by limits.plan: past it and a margin the round counts as
// dead, so its build never plans past it.
func planWithin(ctx context.Context, cfg *config.Config) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(cfg.Limits.PlanMinutes())*roundUnit)
}

func planTookTooLong(ctx context.Context, cfg *config.Config, err error) error {
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}

	return fmt.Errorf("the plan took longer than limits.plan (%d minutes), after which the next commit's build counts the round as dead: raise limits.plan: %w", cfg.Limits.PlanMinutes(), err)
}

// A branch's commit is planned once per run. A failed slice of that round runs again by rerunning its
// suite's build, which is no new round.
func ranAlready(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, stdout io.Writer) (bool, error) {
	if _, ok, err := readPlan(ctx, store, reportID(ch)); err != nil || !ok {
		return false, err
	}

	if t := cfg.Runs[ch.Run].Trigger; t.PullRequests == nil {
		return true, say(stdout, "%s %s: nothing changed since the %s of %s: not run\n", ch.Run, short12(ch.Commit), ch.Run, short12(ch.Commit))
	}

	return true, say(stdout, "%s %s: planned already\n", ch.Run, ch.ID)
}

func supersede(ctx context.Context, ch runs.Change, store *gitstore.Store, tip string, stdout io.Writer) error {
	data, err := json.Marshal(superseded{Change: ch.ID, Commit: ch.Commit, Tip: tip, At: time.Now().UTC().Truncate(time.Second), JobURL: os.Getenv(envJobURL)})
	if err != nil {
		return fmt.Errorf("superseded: %w", err)
	}

	if _, err := store.Write(ctx, "superseded "+ch.Run+" "+short12(ch.Commit),
		gitstore.Update{Ref: supersededRef(ch.Run, ch.Commit), Any: true, Files: map[string][]byte{supersededFile: append(data, '\n')}}); err != nil {
		return err
	}

	return say(stdout, "%s %s: superseded by %s: not run, the newer commit's run covers it\n", ch.Run, short12(ch.Commit), short12(tip))
}

const supersededKeep = 14 * 24 * time.Hour

func sweepSuperseded(ctx context.Context, store *gitstore.Store, now time.Time) error {
	refs, err := store.List(ctx, supersededPrefix, report.AllRounds)
	if err != nil {
		return err
	}

	var ups []gitstore.Update

	for name, ref := range refs {
		if now.Sub(ref.Time) > supersededKeep {
			ups = append(ups, gitstore.Update{Ref: name, Old: ref.Commit, Delete: true})
		}
	}

	if len(ups) == 0 {
		return nil
	}

	_, err = store.Write(ctx, "sweep superseded", ups...)

	return err
}

func supersededOf(ctx context.Context, store *gitstore.Store, run, commit string) (superseded, bool, error) {
	ref, ok, err := store.First(ctx, []string{supersededRef(run, commit)})
	if err != nil || !ok {
		return superseded{}, false, err
	}

	data, ok, err := store.File(ctx, ref.Commit, supersededFile)
	if err != nil || !ok {
		return superseded{}, false, err
	}

	var s superseded
	if err := json.Unmarshal(data, &s); err != nil {
		return superseded{}, false, fmt.Errorf("%s: %w", ref.Name, err)
	}

	return s, true, nil
}
