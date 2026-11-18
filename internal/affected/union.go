package affected

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
)

const baseConfigReason = "selected by the base's configuration"

// Union plans a change that edits the config: what its own config selects plus what the base's selects
// in the same change; a slice or suite only the base's selects runs as the base defined it.
func Union(own, byBase *Plan) *Plan {
	return union(own, byBase, nil)
}

// UnionTrusted plans a trusted run whose config edits the base's. Its config is the branch's own, so
// the base's selection adds only the slices the head's config still has in each suite, and no suite
// the head's config dropped; the plan names each one it leaves out.
func UnionTrusted(own, byBase *Plan, head *config.Config, run string) *Plan {
	return union(own, byBase, &trustedHead{cfg: head, run: run})
}

// trustedHead is the config of a trusted run's head, which alone says what each suite has.
type trustedHead struct {
	cfg *config.Config
	run string
}

func union(own, byBase *Plan, head *trustedHead) *Plan {
	out := *own
	out.Suites = slices.Clone(own.Suites)
	out.Warnings = slices.Clone(own.Warnings)

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
		if i < 0 && head != nil {
			out.Warnings = append(out.Warnings, head.suiteGone(bs)...)

			continue
		}

		if i < 0 {
			bs.Reason = joinReason(bs.Reason, baseConfigReason)
			bs.Reasons = baseReasons(bs.Selected, nil)
			bs.Causes = causesOf(bs)
			out.Suites = append(out.Suites, bs)

			continue
		}

		var notes, checkNotes []string

		bs.Selected, notes = head.keep(out.Suites[i], bs.Selected)
		bs.WholeSuite, checkNotes = head.keepChecks(bs.Suite, bs.WholeSuite)
		out.Warnings = append(append(out.Warnings, notes...), checkNotes...)
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

// keep is what of the base's selection in a suite the head's config still has in it, and a note of
// what it left out; without a trusted head it keeps everything.
func (h *trustedHead) keep(own SuitePlan, selected []bazel.Slice) ([]bazel.Slice, []string) {
	if h == nil {
		return selected, nil
	}

	member := map[string]bool{}
	for _, sl := range own.slices {
		member[sl.Label] = true
	}

	var (
		kept              []bazel.Slice
		excluded, leftOut []string
	)

	s := h.cfg.Suites[own.Suite]

	for _, sl := range selected {
		switch {
		case member[sl.Label]:
			kept = append(kept, sl)
		case excludes(s, sl):
			excluded = append(excluded, sl.Name)
		default:
			leftOut = append(leftOut, sl.Name)
		}
	}

	if !h.cfg.RunCovers(h.run, own.Suite) {
		return kept, nil
	}

	var notes []string
	if len(excluded) > 0 {
		notes = append(notes, leftOutNote(excluded, own.Suite, true))
	}

	if len(leftOut) > 0 {
		notes = append(notes, leftOutNote(leftOut, own.Suite, false))
	}

	return kept, notes
}

func excludes(s config.Suite, sl bazel.Slice) bool {
	if sl.Label == bazel.SelectorLabel(sl.Name) {
		return matchAny(s.Exclude, sl.Name)
	}

	return s.ExcludesTarget(sl.Label)
}

func leftOutNote(names []string, suite string, excluded bool) string {
	it, runs := "it", "it no longer runs there"
	if len(names) > 1 {
		it, runs = "them", "they no longer run there"
	}

	how := "leaves " + it + " out"
	if excluded {
		how = "excludes " + it
	}

	return fmt.Sprintf("%s: the base's configuration had %s in %s; this commit's configuration %s, so %s", strings.Join(names, ", "), it, suite, how, runs)
}

// keepChecks is what of the base's whole-suite checks in a suite the head's config still has, and a
// note of each it left out.
func (h *trustedHead) keepChecks(suite string, checks []string) ([]string, []string) {
	if h == nil {
		return checks, nil
	}

	var kept, notes []string

	for _, w := range checks {
		if slices.ContainsFunc(h.cfg.Suites[suite].WholeSuite, func(c config.WholeSuiteCheck) bool { return c.Name == w }) {
			kept = append(kept, w)
		} else if h.cfg.RunCovers(h.run, suite) {
			notes = append(notes, fmt.Sprintf("whole-suite check %s: the base's configuration had it in %s; this commit's configuration doesn't, so it no longer runs there", w, suite))
		}
	}

	return kept, notes
}

// suiteGone notes a suite the base's config has and the head's doesn't, when the base's selected
// something in it.
func (h *trustedHead) suiteGone(bs SuitePlan) []string {
	if len(bs.Selected) == 0 && len(bs.WholeSuite) == 0 || !h.cfg.RunCovers(h.run, bs.Suite) {
		return nil
	}

	return []string{fmt.Sprintf("suite %s: the base's configuration had it; this commit's configuration doesn't, so it no longer runs", bs.Suite)}
}
