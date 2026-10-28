package cli

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// The sample narrowings are on only with plugins, at a level that has them; sample_on entries sample either way.
func sampleRule(cfg *config.Config, set *plugins.Set) func(suite string) *affected.SampleRule {
	on, entryOn, offAt := false, false, map[string]int{}

	if set != nil {
		on = set.On(plugins.NarrowSample, narrowingLevel(cfg))
		entryOn = set.On(plugins.NarrowPluginEntry, narrowingLevel(cfg))

		for _, n := range []string{affected.SampledBySample, affected.SampledBySampleBound, affected.SampledBySetup, affected.SampledByPluginEntry} {
			offAt[n] = set.OffAt(n)
		}
	}

	return func(suite string) *affected.SampleRule {
		s := cfg.Suites[suite]

		n := s.Sample
		if n == 0 {
			n = s.Parallel
		}

		if n == 0 {
			n = config.DefaultParallel
		}

		return &affected.SampleRule{On: on, EntryOn: entryOn, N: n, OffAt: offAt, Group: func(slice string) string { return report.GroupOf(s.Group, slice) }}
	}
}

// toSample moves the units a suite's recorded slices all bind the same way onto its sample targets: those
// every slice ran to affected.SampleTarget and, with sample-bound, those bound only through the sample or
// at start-up to affected.SampleBoundTarget. A unit a slice binds for another reason too stays that slice's.
func toSample(t *deps.Tree, ss []deps.Slice, sample, bound bool) []deps.Slice {
	if !sample {
		return ss
	}

	reverse := map[string][]string{}
	for _, ln := range t.Links {
		reverse[ln.To] = append(reverse[ln.To], ln.From)
	}

	type key struct{ suite, width string }

	listed := map[key][]int{}

	for i, d := range ss {
		if d.Everything || isTarget(d.Suite, d.Name) {
			continue
		}

		k := key{d.Suite, d.Width}
		listed[k] = append(listed[k], i)
	}

	out := ss

	for _, k := range sortedKeysBy(listed, func(k key) string { return k.suite + "/" + k.width }) {
		idx := listed[k]
		if len(idx) < 2 {
			continue
		}

		common := commonUnits(ss, idx)
		ran := map[string]bool{}

		for id := range common {
			if all(ss, idx, func(d deps.Slice) bool { return d.Why[id].Kind == whyExecuted }) {
				ran[id] = true
			}
		}

		extra := map[string]bool{}

		if bound {
			extra = boundThrough(ss, idx, common, ran, reverse)
		}

		// out, not ss: an append below may have moved the slices.
		for _, i := range idx {
			out[i].Units = without(out[i].Units, ran, extra)
		}

		if len(ran) > 0 {
			out = append(out, deps.Slice{Suite: k.suite, Name: affected.SampleTarget(k.suite), Width: k.width, Units: sortedSet(ran)})
		}

		if len(extra) > 0 {
			out = append(out, deps.Slice{Suite: k.suite, Name: affected.SampleBoundTarget(k.suite), Width: k.width, Units: sortedSet(extra)})
		}
	}

	return out
}

const (
	whyExecuted = "executed"
	whyClosure  = "closure"
	// whyInit is a bind correlator's word for a unit that runs when a package the slice links starts.
	whyInit = "init"
)

// A unit a bind correlator bound without listing its vias can't be judged, and stays.
func boundThrough(ss []deps.Slice, idx []int, common, ran map[string]bool, reverse map[string][]string) map[string]bool {
	in := func(extra map[string]bool) func(string) bool {
		return func(id string) bool { return ran[id] || extra[id] }
	}

	extra := map[string]bool{}

	for grew := true; grew; {
		grew = false
		inSample := in(extra)

		for _, id := range sortedSet(common) {
			if inSample(id) {
				continue
			}

			if all(ss, idx, func(d deps.Slice) bool { return onlyThrough(d, id, reverse, inSample) }) {
				extra[id] = true
				grew = true
			}
		}
	}

	return extra
}

func onlyThrough(d deps.Slice, id string, reverse map[string][]string, inSample func(string) bool) bool {
	w := d.Why[id]

	if w.By != "" && w.Kind == whyInit {
		return true
	}

	var vias []string

	switch {
	case w.By != "":
		if len(w.Vias) == 0 {
			return false
		}

		vias = w.Vias
	case w.Kind != whyClosure:
		return false
	}

	// whatever reaches it over the links within the slice brings it too.
	for _, from := range reverse[id] {
		if _, ok := d.Why[from]; ok {
			vias = append(vias, from)
		}
	}

	if len(vias) == 0 {
		return false
	}

	for _, v := range vias {
		if !inSample(v) {
			return false
		}
	}

	return true
}

func commonUnits(ss []deps.Slice, idx []int) map[string]bool {
	out := map[string]bool{}
	for _, id := range ss[idx[0]].Units {
		out[id] = true
	}

	for _, i := range idx[1:] {
		has := make(map[string]bool, len(ss[i].Units))
		for _, id := range ss[i].Units {
			has[id] = true
		}

		for id := range out {
			if !has[id] {
				delete(out, id)
			}
		}
	}

	return out
}

func all(ss []deps.Slice, idx []int, f func(deps.Slice) bool) bool {
	for _, i := range idx {
		if !f(ss[i]) {
			return false
		}
	}

	return true
}

func without(units []string, a, b map[string]bool) []string {
	out := units[:0:0]

	for _, id := range units {
		if !a[id] && !b[id] {
			out = append(out, id)
		}
	}

	return out
}

func sortedSet(m map[string]bool) []string { return sortedKeys(m) }

func sortedKeysBy[K comparable, V any](m map[K]V, name func(K) string) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.SortFunc(out, func(a, b K) int { return strings.Compare(name(a), name(b)) })

	return out
}

func isTarget(suite, name string) bool {
	return name == recording.SetupTarget(suite) || name == affected.SampleTarget(suite) || name == affected.SampleBoundTarget(suite) ||
		name == affected.EntryTarget(suite)
}

// Each slice keeps what it gave up as dropped by plugin-entry, so the plan names it when a moved entry changed.
func toEntrySample(bs []plugins.BindSlice, ss []deps.Slice, on bool) []deps.Slice {
	if !on {
		return ss
	}

	sampled := map[string]bool{}

	for _, sl := range bs {
		if sl.HasList && (sl.Setup || !strings.HasPrefix(sl.Name, "_")) {
			sampled[sl.Suite+"/"+sl.Name] = true
		}
	}

	type key struct{ suite, width string }

	moved := map[key]map[string]bool{}

	for i := range ss {
		d := &ss[i]
		if !sampled[d.Suite+"/"+d.Name] {
			continue
		}

		var pins []string

		d.Units = slices.DeleteFunc(d.Units, func(id string) bool {
			if !strings.HasPrefix(id, pinPrefix) {
				return false
			}

			pins = append(pins, id)

			return true
		})

		if len(pins) == 0 {
			continue
		}

		if d.Dropped == nil {
			d.Dropped = map[string][]string{}
		}

		d.Dropped[plugins.NarrowPluginEntry] = pins

		k := key{d.Suite, d.Width}
		if moved[k] == nil {
			moved[k] = map[string]bool{}
		}

		for _, id := range pins {
			moved[k][id] = true
		}
	}

	out := ss

	for _, k := range sortedKeysBy(moved, func(k key) string { return k.suite + "/" + k.width }) {
		out = append(out, deps.Slice{Suite: k.suite, Name: affected.EntryTarget(k.suite), Width: k.width, Units: sortedSet(moved[k])})
	}

	return out
}

const pinPrefix = "plugin:"
