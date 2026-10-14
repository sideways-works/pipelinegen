package graph

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// A plugin's changed entry in the config reaches every slice that binds a
// unit its sbom lists, or a correlator's recorded slices and the near ends
// of its links. While plugin-entry is on, a suite's recorded slices go to
// its sample instead.

func entrySampled(sl plugins.BindSlice) bool {
	return sl.HasList && (sl.Setup || !strings.HasPrefix(sl.Name, "_"))
}

func pinPath(name string) []Path {
	return []Path{{Changed: pinPrefix + name, Steps: []Step{{To: pinPrefix + name, Kind: KindPin}}}}
}

func observedEntry(o Options, sl plugins.BindSlice, ch changes) string {
	for _, name := range ch.correlators {
		if o.Observes == nil || o.Observes(sl.Suite, name) {
			return name
		}
	}

	return ""
}

func correlatorSources(l *loaded, ch changes) []source {
	var out []source

	for _, name := range ch.correlators {
		pin, ok := l.ix[pinPrefix+name]
		if !ok {
			continue
		}

		at := map[int32]bool{}

		for _, t := range l.trees {
			if t == nil {
				continue
			}

			for id := range plugins.AnswerUnits(t, name) {
				if i, ok := l.ix[id]; ok {
					at[i] = true
				}
			}
		}

		ids := make([]int32, 0, len(at))
		for i := range at {
			ids = append(ids, i)
		}

		slices.SortFunc(ids, func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })

		for _, i := range ids {
			out = append(out, source{at: i, node: pin, from: i, kind: KindPin})
		}
	}

	return out
}

func entryHow(ch changes) map[int32]string {
	out := make(map[int32]string, len(ch.entryUnits))
	for i := range ch.entryUnits {
		out[i] = HowPin
	}

	return out
}

func entryReach(l *loaded, entry *walk, sl plugins.BindSlice, roots []int32, dependents bool, observed string, n int, undescribed func(string) bool) ([]Path, bool) {
	if sl.HasList && observed != "" {
		return pinPath(observed), true
	}

	if entry == nil {
		return nil, false
	}

	return selectOne(l, entry, nil, sl, roots, dependents, "", false, [2][]string{}, n, undescribed)
}
