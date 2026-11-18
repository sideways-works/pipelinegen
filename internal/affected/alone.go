package affected

import (
	"fmt"
	"maps"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

// AloneReason is a slice bound to nothing but its own file: what the file imports reaches it
// through nothing, so it runs on every change until the config binds it.
const AloneReason = "nothing binds it but its own file (no owner target, no Bazel target holding the file, " +
	"no extra_deps entry, no pipelinegen:data line), so a change to what it imports would select nothing; " +
	"it runs on every change until the config binds it"

func (o Options) alone(h *Snapshot, suite string) map[string]bool {
	if o.Alone == nil {
		return nil
	}

	names := map[string]bool{}
	for _, n := range o.Alone(h.Commit, suite) {
		names[n] = true
	}

	out := map[string]bool{}

	for _, sl := range h.Slices[suite] {
		if names[sl.Name] {
			out[sl.Label] = true
		}
	}

	return out
}

func (o Options) aloneRule(suite string, h *Snapshot, seen map[string]bool, changes []gitx.Change) (alone, out, forced map[string]bool) { //nolint:nonamedreturns // the three sets
	alone = o.alone(h, suite)
	if len(alone) > 0 {
		o.warnf("%s", aloneWarning(suite, h, alone))
	}

	out, forced = withAlone(seen, alone, changes)

	return alone, out, forced
}

func withAlone(seen, alone map[string]bool, changes []gitx.Change) (out, forced map[string]bool) { //nolint:nonamedreturns // the selection and what it forced
	if len(alone) == 0 || len(changes) == 0 {
		return seen, nil
	}

	out, forced = make(map[string]bool, len(seen)+len(alone)), map[string]bool{}
	maps.Copy(out, seen)

	for l := range alone {
		if !out[l] {
			forced[l] = true
		}

		out[l] = true
	}

	return out, forced
}

func aloneReasons(sp *SuitePlan, alone map[string]bool) {
	for _, sl := range sp.Selected {
		if alone[sl.Label] {
			sp.Reasons[sl.Label] = appendNew(sp.Reasons[sl.Label], Reason{Kind: KindEverything, What: AloneReason})
		}
	}
}

func aloneWarning(suite string, h *Snapshot, alone map[string]bool) string {
	var names []string

	for _, sl := range h.Slices[suite] {
		if alone[sl.Label] {
			names = append(names, sl.Name)
		}
	}

	examples := strings.Join(names[:min(2, len(names))], ", ")
	if len(names) > 2 {
		examples += fmt.Sprintf(" and %d more", len(names)-2)
	}

	return fmt.Sprintf("suite %s: nothing binds %d of its test files but the files themselves (%s): no owner target, Bazel target, extra_deps entry "+
		"or pipelinegen:data line holds them, so they run on every change; give the suite an owner, a target that holds them, "+
		"or bind: recorded with a correlator that binds them", suite, len(names), examples)
}

func unforced(selected []bazel.Slice, forced map[string]bool) []bazel.Slice {
	if len(forced) == 0 {
		return selected
	}

	out := make([]bazel.Slice, 0, len(selected))

	for _, sl := range selected {
		if !forced[sl.Label] {
			out = append(out, sl)
		}
	}

	return out
}
