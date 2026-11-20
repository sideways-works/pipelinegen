package report

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Pass is where a slice last passed in a run, with its tests run.
type Pass struct {
	Commit string
	Label  string
	At     time.Time
	// Carried is a pull request's slice its build carried over from the build before, as passed.
	Carried bool
}

// Passes are, by "<suite>/<slice>", each slice's newest pass among commits (newest first): passed with
// tests run, in a run whose plan tested that very commit; records the sweep removed count as none.
func Passes(ctx context.Context, store *gitstore.Store, run, change string, commits []string) (map[string]Pass, error) {
	rank := make(map[string]int, len(commits))
	full := make(map[string]string, len(commits))

	for i, c := range commits {
		rank[short(c)], full[short(c)] = i, c
	}

	runs, plans := RunsOf(run)+RefPart(change)+"/", PlansPrefix(run, change)

	refs, err := store.List(ctx, runs, plans)
	if err != nil {
		return nil, err
	}

	planOf := map[string]string{}

	var stateCommits, planCommits []string

	for name, ref := range refs {
		if c, ok := strings.CutPrefix(name, plans); ok {
			if _, in := full[c]; in {
				planOf[ref.Commit] = c
				planCommits = append(planCommits, ref.Commit)
			}

			continue
		}

		c, rest, _ := strings.Cut(strings.TrimPrefix(name, runs), "/")
		if _, in := full[c]; in && strings.HasSuffix(rest, "/"+stateLeaf) && !strings.HasPrefix(rest, "_") {
			stateCommits = append(stateCommits, ref.Commit)
		}
	}

	tested, err := testedCommits(ctx, store, planCommits, planOf, full)
	if err != nil {
		return nil, err
	}

	data, err := store.FileEach(ctx, stateCommits, stateFile)
	if err != nil {
		return nil, err
	}

	out := map[string]Pass{}

	for name, ref := range refs {
		c, _, _ := strings.Cut(strings.TrimPrefix(name, runs), "/")
		if !strings.HasPrefix(name, runs) || !tested[c] {
			continue
		}

		key, p, ok := passOf(ID{Run: run, Change: change, Commit: full[c]}, name, data[ref.Commit])
		if had, seen := out[key]; ok && (!seen || rank[c] < rank[short(had.Commit)]) {
			out[key] = p
		}
	}

	return out, nil
}

// passOf reads one slice's state as a pass, keyed "<suite>/<slice>".
func passOf(id ID, ref string, raw []byte) (string, Pass, bool) {
	suite, slice, isState, err := id.sliceOf(ref)
	if err != nil || !isState || raw == nil {
		return "", Pass{}, false
	}

	var sf StateFile
	if json.Unmarshal(raw, &sf) != nil || sf.State != Passed || sf.Tests != nil && *sf.Tests == 0 {
		return "", Pass{}, false
	}

	return suite + "/" + slice, Pass{Commit: id.Commit, Label: sf.Label, At: sf.Updated}, true
}

// testedCommits are the commits whose plan tested the commit itself.
func testedCommits(ctx context.Context, store *gitstore.Store, planCommits []string, planOf, full map[string]string) (map[string]bool, error) {
	data, err := store.FileEach(ctx, planCommits, planFileName)
	if err != nil {
		return nil, err
	}

	out := map[string]bool{}

	for commit, c := range planOf {
		plan, err := parsePlan(data[commit])
		if err == nil && plan.Head == full[c] {
			out[c] = true
		}
	}

	return out, nil
}
