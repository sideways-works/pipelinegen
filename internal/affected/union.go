package affected

import (
	"maps"
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/internal/bazel"
)

const baseConfigReason = "selected by the base's configuration"

// Union plans a change that edits the config: what its own config selects plus what the base's selects
// in the same change; a slice or suite only the base's selects runs as the base defined it.
func Union(own, byBase *Plan) *Plan {
	out := *own
	out.Suites = slices.Clone(own.Suites)

	if byBase.All && !out.All {
		out.All, out.Reason = true, byBase.Reason+" ("+baseConfigReason+")"
	}

	// a slice the head's config adds to a suite is new to it: no run of
	// the suite ran it before
	for i, s := range out.Suites {
		j := slices.IndexFunc(byBase.Suites, func(b SuitePlan) bool { return b.Suite == s.Suite })
		if j < 0 {
			out.Suites[i] = addedByConfig(s, nil, AddedSuiteReason)
		} else {
			out.Suites[i] = addedByConfig(s, &byBase.Suites[j], AddedSliceReason)
		}
	}

	for _, bs := range byBase.Suites {
		i := slices.IndexFunc(out.Suites, func(s SuitePlan) bool { return s.Suite == bs.Suite })
		if i < 0 {
			bs.Reason = joinReason(bs.Reason, baseConfigReason)
			bs.Reasons = baseReasons(bs.Selected, nil)
			bs.Causes = causesOf(bs)
			out.Suites = append(out.Suites, bs)

			continue
		}

		out.Suites[i] = unionSuite(out.Suites[i], bs)
	}

	// what the base's levels left out is said with the base's override
	out.Narrowings = slices.Clone(own.Narrowings)

	for _, o := range byBase.Narrowings {
		if !slices.Contains(out.Narrowings, o) {
			out.Narrowings = append(out.Narrowings, o)
		}
	}

	return &out
}

func unionSuite(own, byBase SuitePlan) SuitePlan {
	selected := map[string]bool{}
	for _, sl := range own.Selected {
		selected[sl.Label] = true
	}

	var added []bazel.Slice

	for _, sl := range byBase.Selected {
		if !selected[sl.Label] {
			selected[sl.Label] = true
			added = append(added, sl)
		}
	}

	out := own
	out.Selected = append(slices.Clone(own.Selected), added...)
	sort.Slice(out.Selected, func(i, j int) bool { return out.Selected[i].Label < out.Selected[j].Label })
	out.Untouched = slices.DeleteFunc(slices.Clone(own.Untouched), func(l string) bool { return selected[l] })
	out.Reasons = baseReasons(added, own.Reasons)
	out.Causes = causesOf(out)

	if byBase.All && !own.All {
		out.All, out.Reason = true, joinReason(byBase.Reason, baseConfigReason)
	}

	for _, w := range byBase.WholeSuite {
		if !slices.Contains(out.WholeSuite, w) {
			out.WholeSuite = append(out.WholeSuite, w)
		}
	}

	for _, r := range byBase.Removed {
		if !slices.Contains(out.Removed, r) {
			out.Removed = append(out.Removed, r)
		}
	}

	sort.Strings(out.WholeSuite)
	sort.Strings(out.Removed)
	keepLeftOut(&out)

	return out
}

// The reasons of a slice the head's config adds to a suite, a slice of a
// suite it has (AddedSliceReason) or of one it adds (AddedSuiteReason).
const (
	AddedSliceReason = "the configuration adds it to the suite"
	AddedSuiteReason = "the configuration adds the suite"
)

// addedByConfig selects the slices a suite has under the head's config and not the base's: a test the
// config adds never ran in the suite, so it runs as a new slice does.
func addedByConfig(own SuitePlan, byBase *SuitePlan, why string) SuitePlan {
	known := map[string]bool{}

	if byBase != nil {
		for _, sl := range byBase.Selected {
			known[sl.Label] = true
		}

		for _, l := range byBase.Untouched {
			known[l] = true
		}
	}

	selected := map[string]bool{}
	for _, sl := range own.Selected {
		selected[sl.Label] = true
	}

	var added []bazel.Slice

	for _, sl := range own.slices {
		if !known[sl.Label] && !selected[sl.Label] {
			added = append(added, sl)
		}
	}

	if len(added) == 0 {
		return own
	}

	out := own
	out.Selected = append(slices.Clone(own.Selected), added...)
	sort.Slice(out.Selected, func(i, j int) bool { return out.Selected[i].Label < out.Selected[j].Label })
	out.Untouched = slices.DeleteFunc(slices.Clone(own.Untouched), func(l string) bool {
		return slices.ContainsFunc(added, func(sl bazel.Slice) bool { return sl.Label == l })
	})
	out.Reasons = maps.Clone(own.Reasons)

	if out.Reasons == nil {
		out.Reasons = map[string][]Reason{}
	}

	for _, sl := range added {
		out.Reasons[sl.Label] = append(out.Reasons[sl.Label], Reason{Kind: KindNewSlice, What: why})
	}

	if len(out.Untouched) == 0 && !out.All {
		out.All, out.Reason = true, ReachesEverySlice
	}

	out.Causes = causesOf(out)
	keepLeftOut(&out)

	return out
}

func baseReasons(added []bazel.Slice, own map[string][]Reason) map[string][]Reason {
	out := make(map[string][]Reason, len(own)+len(added))
	maps.Copy(out, own)

	for _, sl := range added {
		out[sl.Label] = append(out[sl.Label], Reason{Kind: KindBaseConfig, What: baseConfigReason})
	}

	return out
}

func joinReason(r, more string) string {
	if r == "" {
		return more
	}

	return r + "; " + more
}
