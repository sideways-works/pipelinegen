package report

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Build is a pull request's last build: the tree it tested and the slices that passed in it.
type Build struct {
	// Commit is the change's commit the build tested, as its refs name it (short); Merged the same in
	// full, as its plan records it ("" for a plan written before plans recorded it).
	Commit, Merged string
	// Base is the target's tip the build merged the change into, Head the merge it tested.
	Base, Head string
	// Passes are, by "<suite>/<slice>", the slices that passed in it with their tests run, and those
	// it carried over from the build before (Carried).
	Passes map[string]Pass
}

// ErrUnreadable is a last build whose records can't be read whole: nothing is carried from it.
var ErrUnreadable = errors.New("its records can't be read whole")

// LastBuild is the newest build of a run's change other than commit's that has results: a slice that
// ended, or one it carried over. A build without either is passed over. False when there is none; an
// error when the newest one's plan or states can't be read, so that nothing is carried from an
// older one either.
func LastBuild(ctx context.Context, store *gitstore.Store, run, change, commit string) (Build, bool, error) {
	runsAt, plansAt := RunsOf(run)+RefPart(change)+"/", PlansPrefix(run, change)

	refs, err := store.List(ctx, runsAt, plansAt)
	if err != nil {
		return Build{}, false, err
	}

	for _, r := range buildsOf(refs, runsAt, plansAt, short(commit)) {
		b, has, err := readBuild(ctx, store, run, change, r)
		if err != nil || has {
			return b, err == nil, err
		}
	}

	return Build{}, false, nil
}

// A build's refs: its plan and its slices' states.
type buildRefs struct {
	commit string
	plan   *gitstore.Ref
	states map[string]gitstore.Ref
	newest time.Time
}

// when orders builds as the sweep does: by when the plan was written, else by the newest record.
func (b buildRefs) when() time.Time {
	if b.plan != nil {
		return b.plan.Time
	}

	return b.newest
}

// buildsOf groups the refs by build, newest first, leaving out the build of skip.
func buildsOf(refs map[string]gitstore.Ref, runsAt, plansAt, skip string) []buildRefs {
	by := map[string]*buildRefs{}

	get := func(c string) *buildRefs {
		if by[c] == nil {
			by[c] = &buildRefs{commit: c, states: map[string]gitstore.Ref{}}
		}

		return by[c]
	}

	for name, ref := range refs {
		if c, ok := strings.CutPrefix(name, plansAt); ok && !strings.Contains(c, "/") {
			b := get(c)
			b.plan = &ref

			continue
		}

		c, rest, _ := strings.Cut(strings.TrimPrefix(name, runsAt), "/")
		if !strings.HasPrefix(name, runsAt) || strings.HasPrefix(rest, "_") {
			continue
		}

		b := get(c)
		if ref.Time.After(b.newest) {
			b.newest = ref.Time
		}

		if strings.HasSuffix(rest, "/"+stateLeaf) {
			b.states[name] = ref
		}
	}

	out := make([]buildRefs, 0, len(by))

	for c, b := range by {
		if c != skip {
			out = append(out, *b)
		}
	}

	slices.SortFunc(out, func(a, b buildRefs) int {
		return cmp.Or(b.when().Compare(a.when()), strings.Compare(b.commit, a.commit))
	})

	return out
}

// readBuild reads one build whole, telling whether it has results.
func readBuild(ctx context.Context, store *gitstore.Store, run, change string, r buildRefs) (Build, bool, error) {
	if r.plan == nil {
		return Build{}, false, fmt.Errorf("the build of %s: %w: it has no plan", r.commit, ErrUnreadable)
	}

	raw, ok, err := store.File(ctx, r.plan.Commit, planFileName)
	if err != nil {
		return Build{}, false, err
	}

	plan, err := parsePlan(raw)
	if !ok || err != nil {
		return Build{}, false, fmt.Errorf("the build of %s: %w: its plan: %w", r.commit, ErrUnreadable, cmp.Or(err, ErrNoPlan))
	}

	commits := make([]string, 0, len(r.states))
	for _, ref := range r.states {
		commits = append(commits, ref.Commit)
	}

	data, err := store.FileEach(ctx, commits, stateFile)
	if err != nil {
		return Build{}, false, err
	}

	id := ID{Run: run, Change: change, Commit: r.commit}
	b := Build{Commit: r.commit, Merged: plan.Merged, Base: plan.Base, Head: plan.Head, Passes: map[string]Pass{}}
	ended := map[string]bool{}

	for name, ref := range r.states {
		suite, slice, isState, err := id.sliceOf(name)

		var sf StateFile
		if err != nil || !isState || data[ref.Commit] == nil || json.Unmarshal(data[ref.Commit], &sf) != nil {
			return Build{}, false, fmt.Errorf("the build of %s: %w: the state of %s", r.commit, ErrUnreadable, name)
		}

		if sf.State == Pending || sf.State == Running {
			continue
		}

		ended[suite+"/"+slice] = true

		if sf.State == Passed && (sf.Tests == nil || *sf.Tests > 0) {
			b.Passes[suite+"/"+slice] = Pass{Commit: r.commit, Label: sf.Label, At: sf.Updated}
		}
	}

	carried := 0

	for _, sp := range plan.Suites {
		for _, n := range sp.Narrowed {
			if n.CarriedFrom == "" {
				continue
			}

			for _, name := range n.Dropped {
				// a slice that ran after all stands by its own state
				if key := sp.Suite + "/" + name; !ended[key] {
					b.Passes[key] = Pass{Commit: r.commit, Carried: true}
					carried++
				}
			}
		}
	}

	return b, len(ended)+carried > 0, nil
}
