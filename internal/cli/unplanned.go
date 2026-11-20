package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/forgeplugin"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The pull requests' check can order a new head behind planned ones, unseen by planning: a head to plan
// gets a checkout in <out>/plan/<number>, one to hand off again the resource's files in <out>/handoff/.
func runUnplanned(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c               common
		run, out, cache string
	)

	fs := flag.NewFlagSet("unplanned", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &run, "run", "", "the pull request run, by its name in the config")
	pathVar(fs, &out, "out", "sweep", "where each head's directory goes", writes)
	pathVar(fs, &cache, "cache", defaultCache(), "where the store's scratch repository waits for the next sweep", reads|writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	start := time.Now()

	cfg, err := c.load()
	if err != nil {
		return err
	}

	// after the store's scratch repository is back in the cache.
	defer trimAfter(ctx, cfg, c.root, cache, start)

	r, ok := cfg.Runs[run]
	if !ok || r.Trigger.PullRequests == nil {
		return fmt.Errorf("%w: -run names no pull request run of the config: %q", errUsage, run)
	}

	pr := r.Trigger.PullRequests
	gh := sweepHost(cfg)

	heads, err := openHeads(ctx, cfg, gh, pr)
	if err != nil {
		return err
	}

	store := openStore(cfg)
	store.Seed = storeSeed(cache, store.Remote)

	defer closeStore(store, &err)

	refs, err := headRecords(ctx, store, run, heads)
	if err != nil {
		return err
	}

	marks, err := sweptMarks(ctx, store, run)
	if err != nil {
		return err
	}

	sw := sweep{
		cfg: cfg, gh: gh, refs: refs, marks: marks, out: out, stdout: stdout, now: time.Now(), wait: pr.Missed(),
		planning: time.Duration(cfg.PlanningLength()) * time.Minute, byForge: cfg.ChangesFromForge(),
	}

	for _, h := range heads {
		ups, taken, err := sw.take(ctx, run, h)
		if err != nil {
			return err
		}

		if taken {
			if err := sw.keep(ctx, store, h, ups...); err != nil {
				return err
			}
		}
	}

	return writeMarks(ctx, store, run, goneMarks(run, heads, marks))
}

// sweepHost posts a head's last word with the token the sweep lists with
// when the sweep holds no report token of its own.
func sweepHost(cfg *config.Config) *gitHubHost {
	gh := newGitHubHost(cfg)
	if gh.tokens[plugin.PurposeReport] == "" {
		gh.tokens[plugin.PurposeReport] = gh.tokens[plugin.PurposeChanges]
	}

	return gh
}

// Of two sweeps taking a head up at once only the first mark written stands, and a planning build that
// began meanwhile has the head; the other sweep removes what it wrote of the head.
func (sw sweep) keep(ctx context.Context, store *gitstore.Store, h runs.Head, ups ...gitstore.Update) error {
	_, err := store.Write(ctx, "sweep "+h.Label(), ups...)

	switch {
	case errors.Is(err, gitstore.ErrStale):
		for _, kind := range []string{sweepPlanDir, sweepHandOffDir} {
			if err := os.RemoveAll(filepath.Join(sw.out, kind, headDir(h))); err != nil {
				return fmt.Errorf("unplanned: %w", err)
			}
		}

		return say(sw.stdout, "%s %s: another sweep or a planning build took it up first\n", h.Label(), short12(h.Commit))
	case err != nil:
		return fmt.Errorf("unplanned: %w", err)
	}

	return nil
}

const (
	sweepPlanDir    = "plan"
	sweepHandOffDir = "handoff"
)

func writeMarks(ctx context.Context, store *gitstore.Store, run string, ups []gitstore.Update) error {
	if len(ups) == 0 {
		return nil
	}

	if _, err := store.Write(ctx, "sweep "+run, ups...); err != nil {
		return fmt.Errorf("unplanned: %w", err)
	}

	return nil
}

// headRecords lists only the open heads' records: a month of closed ones stays put.
func headRecords(ctx context.Context, store *gitstore.Store, run string, heads []runs.Head) (map[string]gitstore.Ref, error) {
	if len(heads) == 0 {
		return map[string]gitstore.Ref{}, nil
	}

	prefixes := make([]string, 0, 3*len(heads))
	for _, h := range heads {
		id := headID(run, h)
		prefixes = append(prefixes, report.PlansOfChange(run, id.Change), id.Prefix(), report.RoundsPrefix(run, id.Change))
	}

	refs, err := store.List(ctx, prefixes...)
	if err != nil {
		return nil, fmt.Errorf("unplanned: %w", err)
	}

	return refs, nil
}

// The next sweep leaves the head alone for missed_after, doubled each time, so a head is taken up a
// bounded number of times. commit is the mark's own, so of two sweeps at once one writes its mark.
type swept struct {
	At     time.Time `json:"at"`
	Times  int       `json:"times"`
	commit string
}

func sweptMarks(ctx context.Context, store *gitstore.Store, run string) (map[string]swept, error) {
	refs, err := store.List(ctx, report.SweptOf(run))
	if err != nil {
		return nil, fmt.Errorf("unplanned: %w", err)
	}

	marks := map[string]swept{}

	for name, ref := range refs {
		if marks[name], err = readMark(ctx, store, ref); err != nil {
			return nil, err
		}
	}

	return marks, nil
}

func headMark(ctx context.Context, store *gitstore.Store, id report.ID) (swept, error) {
	ref, ok, err := store.First(ctx, []string{report.SweptRef(id)})
	if err != nil || !ok {
		return swept{}, err
	}

	return readMark(ctx, store, ref)
}

// A mark that won't read counts as a mark of no time, which a sweep writes over.
func readMark(ctx context.Context, store *gitstore.Store, ref gitstore.Ref) (swept, error) {
	data, ok, err := store.File(ctx, ref.Commit, sweptFile)
	if err != nil {
		return swept{}, fmt.Errorf("unplanned: %w", err)
	}

	m := swept{commit: ref.Commit}
	if ok {
		if err := json.Unmarshal(data, &m); err != nil {
			m = swept{commit: ref.Commit}
		}
	}

	return m, nil
}

const sweptFile = "swept.json"

func goneMarks(run string, heads []runs.Head, marks map[string]swept) []gitstore.Update {
	open := map[string]bool{}
	for _, h := range heads {
		open[report.SweptRef(headID(run, h))] = true
	}

	var ups []gitstore.Update

	for name := range marks {
		if !open[name] {
			ups = append(ups, gitstore.Update{Ref: name, Delete: true, Any: true})
		}
	}

	return ups
}

type sweep struct {
	cfg    *config.Config
	gh     *gitHubHost
	refs   map[string]gitstore.Ref
	marks  map[string]swept
	out    string
	stdout io.Writer
	now    time.Time
	wait   time.Duration
	// planning is how long a head whose planning began is left to that
	// build: the plan's limit and a margin.
	planning time.Duration
	// byForge heads are found through the code's forge alone, so a head with no plan is planned at
	// once, and a hand-off is a run.json.
	byForge bool
}

// After maxTakes the sweep says so once, on the head's run status, and leaves the head.
const maxTakes = 3

func (sw sweep) take(ctx context.Context, run string, h runs.Head) ([]gitstore.Update, bool, error) {
	id := headID(run, h)
	dir := headDir(h)
	markRef := report.SweptRef(id)
	mark := sw.marks[markRef]

	if held, why := sw.held(mark); held {
		return nil, false, say(sw.stdout, "%s %s: %s\n", h.Label(), short12(h.Commit), why)
	}

	_, planned := sw.refs[id.PlanRef()]
	began, planning := sw.refs[id.RoundRef(report.RoundStarted)]

	// a head whose plan is on its way, however slow, is its build's: only
	// once the plan's limit has passed does it count as missed.
	if since := sw.now.Sub(began.Time); !planned && planning && since < sw.planning {
		return nil, false, say(sw.stdout, "%s %s: its planning began %s ago: left to its build\n", h.Label(), short12(h.Commit), since.Round(time.Second))
	}

	if mark.Times == maxTakes && (!planned || !begun(sw.refs, id)) {
		return []gitstore.Update{sw.marked(markRef, mark)}, true, sw.giveUp(ctx, run, h, planned)
	}

	var err error

	switch {
	case !planned && !sw.byForge && sw.now.Sub(h.Updated) < sw.wait:
		return nil, false, say(sw.stdout, "%s %s: waits for its own plan\n", h.Label(), short12(h.Commit))
	case !planned && sw.byForge:
		if err = handOff(sw.cfg, id, h, filepath.Join(sw.out, sweepPlanDir, dir)); err == nil {
			err = say(sw.stdout, "%s %s: no plan: plan it\n", h.Label(), short12(h.Commit))
		}
	case !planned:
		var admitted bool
		if admitted, err = sw.planHead(ctx, id, h, filepath.Join(sw.out, sweepPlanDir, dir)); err != nil || !admitted {
			// a head the run leaves out is marked too: Admit asks GitHub
			// a lot, and its answer rarely changes within missed_after.
			return []gitstore.Update{sw.marked(markRef, mark)}, err == nil, err
		}
	default:
		again, why := sw.stalled(id)
		if !again {
			return nil, false, say(sw.stdout, "%s %s: %s\n", h.Label(), short12(h.Commit), why)
		}

		if sw.byForge {
			err = handOff(sw.cfg, id, h, filepath.Join(sw.out, sweepHandOffDir, dir))
		} else {
			err = h.Files(filepath.Join(sw.out, sweepHandOffDir, dir))
		}

		if err == nil {
			err = say(sw.stdout, "%s %s: %s\n", h.Label(), short12(h.Commit), why)
		}
	}

	ups := []gitstore.Update{sw.marked(markRef, mark)}

	// the head this sweep plans: its planning began now, over a record of
	// planning that ended without a plan.
	if err == nil && !planned {
		var round gitstore.Update
		if round, err = report.RoundUpdate(id, report.RoundStarted, "", began.Commit); err == nil {
			ups = append(ups, round)
		}
	}

	return ups, err == nil, err
}

func (sw sweep) held(mark swept) (bool, string) {
	if mark.Times > maxTakes {
		return true, fmt.Sprintf("given up after %d times", maxTakes)
	}

	return sw.lately(mark)
}

func (sw sweep) lately(mark swept) (bool, string) {
	if !mark.At.IsZero() && sw.now.Sub(mark.At) < sw.wait<<min(mark.Times, maxTakes) {
		return true, fmt.Sprintf("taken up %d times, the last at %s: waits", mark.Times, mark.At.UTC().Format(time.RFC3339))
	}

	return false, ""
}

// The sweep and a planning build that finds its head planned both decide by stalled, so a head is
// handed on again by one rule.
func (sw sweep) stalled(id report.ID) (bool, string) {
	switch {
	case begun(sw.refs, id):
		short, ok := shortOfMemory(sw.refs, id)
		if !ok {
			return false, "planned, a suite began"
		}

		if sw.now.Sub(short) < sw.wait {
			return false, "planned, its run ended with slices not run for want of memory: waits for the workers"
		}

		return true, "planned, its run ended with slices not run for want of memory: hand it off again"
	case sw.now.Sub(sw.refs[id.PlanRef()].Time) < sw.wait:
		return false, "planned, its suites on their way"
	}

	return true, "planned, no suite began: hand it off again"
}

// giveUp says once, on the head's run status, that the sweep leaves it, so a head that can never start
// isn't retried in silence.
func (sw sweep) giveUp(ctx context.Context, run string, h runs.Head, planned bool) error {
	why := fmt.Sprintf("no plan after %d tries of the sweep: see the sweep job", maxTakes)
	if planned {
		why = fmt.Sprintf("planned, but no suite began after %d hand-offs: see the suite jobs", maxTakes)
	}

	var gh report.GitHub = sw.gh
	if sw.byForge {
		// a plain git server has no status to post: the line below and
		// `pipelinegen status` say it.
		gh = host(sw.cfg)
	}

	if err := gh.SetStatus(ctx, h.Commit, report.CommitStatus{State: "error", Context: report.RunContext(sw.cfg, run), Description: why}); err != nil {
		return fmt.Errorf("unplanned: %s: %w", h.Label(), err)
	}

	return say(sw.stdout, "%s %s: given up: %s\n", h.Label(), short12(h.Commit), why)
}

func (sw sweep) marked(ref string, last swept) gitstore.Update {
	data := fmt.Appendf(nil, "{\"at\": %q, \"times\": %d}\n", sw.now.UTC().Format(time.RFC3339Nano), last.Times+1)

	return gitstore.Update{Ref: ref, Old: last.commit, Files: map[string][]byte{sweptFile: data}}
}

func openHeads(ctx context.Context, cfg *config.Config, gh runs.Changes, pr *config.PullRequests) ([]runs.Head, error) {
	if !cfg.ChangesFromForge() {
		return runs.OpenHeads(ctx, gh, pr)
	}

	cc := cfg.CodeForge()
	remote := forgeRemote(cfg)

	// listing a plain git server's branches is reading it: git does, with
	// the read credential, and the forge picks the changes among them.
	env := runs.AuthEnv(runs.Auth{Scope: httpsScope(remote), Token: tokenFor(cfg, plugin.PurposeRead), Env: sshEnv(cfg, plugin.PurposeRead)})

	branches, err := runs.Branches(ctx, remote, env)
	if err != nil {
		return nil, err
	}

	host, err := openForge(ctx, cfg, config.ForgeGit, forgeplugin.Options{
		Config: map[string]any{"branches": cc.Changes.Branches, "heads": branches, "links": cc.Links}, Remote: remote,
	})
	if err != nil {
		return nil, err
	}

	changes, err := host.Changes(ctx, plugin.ChangesArgs{Base: pr.Base, Filters: plugin.ChangesFilters{Head: pr.Head}})
	if err != nil {
		return nil, err
	}

	var heads []runs.Head

	for _, c := range changes {
		// a change runs only at a branch git listed, at the commit git saw
		if !forgeplugin.Admit(c) || c.HeadBranch == pr.Base || branches[c.HeadBranch] != c.Head {
			continue
		}

		if pr.Head != "" {
			if ok, err := path.Match(pr.Head, c.HeadBranch); err != nil || !ok {
				continue
			}
		}

		heads = append(heads, runs.Head{Change: cc.Name + "/" + c.ID, Commit: c.Head, Branch: c.HeadBranch})
	}

	return heads, nil
}

// httpsScope is an https remote without a user, the scope its token goes
// to; "" for any other remote.
func httpsScope(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}

	u.User = nil

	return u.String()
}

func handOff(cfg *config.Config, id report.ID, h runs.Head, dir string) error {
	return runs.WriteRunFile(dir, runs.Change{
		Run: id.Run, ID: id.Change, Commit: h.Commit, Branch: h.Branch, Base: cfg.Runs[id.Run].Trigger.PullRequests.Base,
		// where the change is fetched from, as the trusted config says:
		// the steps after this one read the change's own config.
		Forge: cfg.CodeForge().Name, Remote: forgeRemote(cfg),
		Handed: handedNow(),
	})
}

// handedNow is a hand-off's time, with a random part so that two in the
// same instant differ too.
func handedNow() string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce) // never fails on Linux; a zero part still has the time

	return time.Now().UTC().Format(time.RFC3339Nano) + "-" + hex.EncodeToString(nonce)
}

func forgeRemote(cfg *config.Config) string {
	if r := cfg.CodeForge().Remote; r != "" {
		return r
	}

	return codeRemote(cfg)
}

func headDir(h runs.Head) string {
	if h.Change != "" {
		return report.RefPart(h.Change)
	}

	return strconv.Itoa(h.PR)
}

func headID(run string, h runs.Head) report.ID {
	return report.ID{Run: run, Change: h.ID(), Commit: h.Commit, PR: h.PR}
}

func (sw sweep) planHead(ctx context.Context, id report.ID, h runs.Head, dir string) (bool, error) {
	cfg, stdout := sw.cfg, sw.stdout
	ch := runs.Change{Run: id.Run, ID: id.Change, Commit: h.Commit, PR: h.PR, Branch: h.Branch, Base: cfg.Runs[id.Run].Trigger.PullRequests.Base}

	admitted, why, err := runs.Admit(ctx, sw.gh, cfg, ch, plannedSince(sw.refs, id))
	if err != nil {
		return false, err
	}

	if !admitted {
		return false, say(stdout, "%s %s: no plan, and none to make: %s\n", h.Label(), short12(h.Commit), why)
	}

	if err := runs.CheckoutHead(ctx, dir, codeRemote(cfg), h, runs.Auth{Scope: runs.GitHubScope, Token: tokenFor(cfg, plugin.PurposeRead), Env: sshEnv(cfg, plugin.PurposeRead)}); err != nil {
		return false, err
	}

	return true, say(stdout, "%s %s: no plan: plan it\n", h.Label(), short12(h.Commit))
}

// shortOfMemory tells a run whose last word is its short-of-memory mark (no suite build began or ended
// after it, all have ended, none by hand), and when the mark was written.
func shortOfMemory(refs map[string]gitstore.Ref, id report.ID) (time.Time, bool) {
	short, ok := refs[id.ShortRef()]
	if _, ended := refs[endedRef(id)]; !ok || ended {
		return time.Time{}, false
	}

	for name, ref := range refs {
		suite, began := strings.CutPrefix(name, id.BeganRef(""))
		if !began && !strings.HasPrefix(name, id.EndRef("")) {
			continue
		}

		if short.Time.Before(ref.Time) {
			return time.Time{}, false
		}

		if end, ok := refs[id.EndRef(suite)]; began && (!ok || end.Time.Before(ref.Time)) {
			return time.Time{}, false
		}
	}

	return short.Time, true
}

func begun(refs map[string]gitstore.Ref, id report.ID) bool {
	for name := range refs {
		if strings.HasPrefix(name, id.Prefix()) {
			return true
		}
	}

	return false
}
