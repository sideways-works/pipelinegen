package report

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// defaultAlarm is how many failures may trace to narrowed changes before a
// run of everything recommends a narrowing level.
const defaultAlarm = 2

type planRef struct {
	run, change, commit string
	when                time.Time
	plan                *planFile
}

func readPlans(ctx context.Context, store *gitstore.Store, cfg *config.Config, refs map[string]gitstore.Ref) (map[string]planRef, error) {
	var commits []string

	for name, ref := range refs {
		if strings.HasPrefix(name, refRoot+"plans/") {
			commits = append(commits, ref.Commit)
		}
	}

	data, err := store.FileEach(ctx, commits, planFileName)
	if err != nil {
		return nil, err
	}

	out := map[string]planRef{}

	for name, ref := range refs {
		k, ok := runOf(name)
		raw, has := data[ref.Commit]

		if !ok || !has || !strings.HasPrefix(name, refRoot+"plans/") {
			continue
		}

		var p planFile
		if json.Unmarshal(raw, &p) != nil {
			continue
		}

		p.ofRun(cfg, k.run)

		out[name] = planRef{run: k.run, change: k.change, commit: k.commit, when: ref.Time, plan: &p}
	}

	return out, nil
}

func passedRuns(ctx context.Context, store *gitstore.Store, id ID, refs map[string]gitstore.Ref, plans map[string]planRef) (map[runKey]bool, error) {
	var commits []string

	for name, ref := range refs {
		if strings.HasPrefix(name, refRoot+"runs/"+id.Run+"/") && strings.HasSuffix(name, "/"+stateLeaf) {
			commits = append(commits, ref.Commit)
		}
	}

	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return nil, err
	}

	passed := map[runKey]int{}
	broken := map[runKey]bool{}

	for name, ref := range refs {
		raw, ok := data[ref.Commit]
		if !ok || !strings.HasSuffix(name, "/"+stateLeaf) {
			continue
		}

		k, ok := runOf(name)
		if !ok {
			continue
		}

		var sf StateFile
		if json.Unmarshal(raw, &sf) != nil || sf.State != Passed {
			broken[k] = true

			continue
		}

		passed[k]++
	}

	out := map[runKey]bool{}

	for _, p := range plans {
		k := runKey{p.run, p.change, p.commit}
		if p.run != id.Run {
			continue
		}

		selected := 0
		for _, sp := range p.plan.Suites {
			selected += len(items(sp))
		}

		out[k] = !broken[k] && passed[k] == selected
	}

	return out, nil
}

func (r *Reporter) net(ctx context.Context, run Run, snap snapshot) (*Net, error) {
	if !snap.plan.All {
		return nil, nil //nolint:nilnil // a run that didn't select everything has no net
	}

	failing := map[string]bool{}

	for _, s := range run.Suites {
		for _, sl := range s.Slices {
			if bad(sl.State) {
				failing[s.Name+"/"+sl.Name] = true
			}
		}
	}

	if len(failing) == 0 {
		return nil, nil //nolint:nilnil // nothing failed, nothing to trace
	}

	refs, err := r.Store.List(ctx, refRoot+"plans/", refRoot+"runs/"+r.ID.Run+"/")
	if err != nil {
		return nil, err
	}

	plans, err := readPlans(ctx, r.Store, r.Config, refs)
	if err != nil {
		return nil, err
	}

	passed, err := passedRuns(ctx, r.Store, r.ID, refs, plans)
	if err != nil {
		return nil, err
	}

	this := refs[r.ID.PlanRef()].Time

	var since time.Time

	for _, p := range plans {
		if p.run == r.ID.Run && p.when.Before(this) && passed[runKey{p.run, p.change, p.commit}] && p.when.After(since) {
			since = p.when
		}
	}

	net := &Net{Alarm: defaultAlarm}
	if r.Config != nil && r.Config.NarrowingAlarm > 0 {
		net.Alarm = r.Config.NarrowingAlarm
	}

	// a pull request's run landed when its recordings were attached to
	// the commit its merge made: that commit, and that time, place it
	landed, err := landedRuns(ctx, r.Store, since)
	if err != nil {
		return nil, err
	}

	net.Traces = r.traces(ctx, plans, landed, failing, since, this)

	return net, nil
}

func (r *Reporter) traces(ctx context.Context, plans map[string]planRef, landed map[runKey]landing, failing map[string]bool, since, this time.Time) []Trace {
	var out []Trace

	subjects := map[string]string{}

	for _, p := range sortedPlans(plans) {
		if p.run == r.ID.Run {
			continue
		}

		when, change, head := p.when, r.ID.Change, p.plan.Head

		if p.change != RefPart(r.ID.Change) {
			l, ok := landed[runKey{p.run, p.change, p.commit}]
			if !ok {
				continue
			}

			when, change, head = l.when, l.change, l.commit
		}

		if !when.After(since) || when.After(this) {
			continue
		}

		for _, t := range dropped(p, failing) {
			t.Change, t.Subject = change, r.subject(ctx, subjects, head)
			out = append(out, t)
		}
	}

	return out
}

type landing struct {
	change, commit string
	when           time.Time
}

func landedRuns(ctx context.Context, store *gitstore.Store, since time.Time) (map[runKey]landing, error) {
	got, err := attached.Since(ctx, store, since)
	if err != nil {
		return nil, err
	}

	out := map[runKey]landing{}

	for _, l := range got {
		for _, a := range l.Runs {
			k := runKey{a.Run, RefPart(a.Change), short(a.Commit)}
			if _, seen := out[k]; !seen {
				out[k] = landing{change: a.Change, commit: l.Commit, when: l.Time}
			}
		}
	}

	return out, nil
}

func dropped(p planRef, failing map[string]bool) []Trace {
	var out []Trace

	for _, sp := range p.plan.Suites {
		for _, n := range sp.Narrowed {
			var set *NarrowingSet
			if lv, ok := SetBy(p.plan.Narrowings, n.Narrowing, n.OffAt); ok {
				set = &lv
			}

			for _, slice := range n.Dropped {
				if failing[sp.Suite+"/"+slice] {
					out = append(out, Trace{
						Suite: sp.Suite, Slice: slice, Run: p.run, Commit: p.commit,
						Kind: n.Kind, Narrowing: n.Narrowing, OffAt: n.OffAt, Set: set,
					})
				}
			}
		}
	}

	return out
}

// sortedPlans orders plans oldest first, so a slice's first trace is the
// change that first left it out.
func sortedPlans(plans map[string]planRef) []planRef {
	out := make([]planRef, 0, len(plans))
	for _, p := range plans {
		out = append(out, p)
	}

	slices.SortFunc(out, func(a, b planRef) int {
		if c := a.when.Compare(b.when); c != 0 {
			return c
		}

		return strings.Compare(a.commit, b.commit)
	})

	return out
}

func (r *Reporter) subject(ctx context.Context, seen map[string]string, sha string) string {
	if sha == "" {
		return ""
	}

	if s, ok := seen[sha]; ok {
		return s
	}

	s, err := r.GitHub.CommitSubject(ctx, sha)
	if err != nil {
		r.warnf("subject of %s: %v", short(sha), err)
	}

	seen[sha] = s

	return s
}
