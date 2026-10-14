package graph

import (
	"slices"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func leftOut(l *loaded, ss []plugins.BindSlice, widths []string, o Options, sel *Selection) {
	add := func(n string, k Key, ps []Path) {
		if _, ok := sel.Selected[k]; ok {
			return
		}

		if sel.LeftOut[n] == nil {
			sel.LeftOut[n] = map[Key][]Path{}
		}

		sel.LeftOut[n][k] = ps
	}

	for _, n := range ruledNarrowings(l) {
		wide := selectAll(load(withRuledOut(l.trees[base], n), withRuledOut(l.trees[head], n)), ss, widths, o)
		for k, ps := range wide.Selected {
			add(n, k, ps)
		}
	}

	byUnitsLeftOut(l, ss, widths, add)

	for _, n := range []string{narrowUnreachedNew, narrowReferencedNew} {
		if on(o, n) {
			namesLeftOut(l, ss, widths, o, n, add)
		}
	}
}

func namesLeftOut(l *loaded, ss []plugins.BindSlice, widths []string, o Options, n string, add func(string, Key, []Path)) {
	wide := o
	wide.On = func(m string) bool { return m != n && on(o, m) }

	for _, width := range widths {
		ch := changesOf(l, wide, width)

		var sources []source

		for _, g := range namesChanged(l, ss, width, wide) {
			if _, ok := ch.how[g]; !ok {
				sources = append(sources, source{at: g, node: g, executed: true})
			}
		}

		if len(sources) == 0 {
			continue
		}

		w := walkFrom(l, width == Dependents, sources)

		for _, sl := range ss {
			if !sl.HasList {
				continue
			}

			for _, r := range rootsOf(l, sl) {
				if c, at := w.reached(r); c >= 0 {
					add(n, Key{Suite: sl.Suite, Name: sl.Name, Width: width}, []Path{w.path(r, at)})

					break
				}
			}
		}
	}
}

func ruledNarrowings(l *loaded) []string {
	var out []string

	for _, t := range l.trees {
		if t == nil {
			continue
		}

		for _, m := range []map[string][]plugin.Link{t.RuledOut, t.TypedRuledOut} {
			for n := range m {
				if !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
	}

	slices.Sort(out)

	return out
}

func withRuledOut(t *deps.Tree, n string) *deps.Tree {
	if t == nil {
		return nil
	}

	c := *t
	c.Links = append(slices.Clone(t.Links), t.RuledOut[n]...)
	c.Typed = append(slices.Clone(t.Typed), t.TypedRuledOut[n]...)
	c.RuledOut, c.TypedRuledOut = nil, nil

	return &c
}

func byUnitsLeftOut(l *loaded, ss []plugins.BindSlice, widths []string, add func(string, Key, []Path)) {
	for _, w := range widths {
		ch := changesOf(l, Options{}, w)

		for _, sl := range ss {
			if !sl.HasList {
				continue
			}

			for _, label := range sl.Data {
				if n, id, ok := changedFileUnit(l, ch, label); ok {
					add(n, Key{Suite: sl.Suite, Name: sl.Name, Width: w}, []Path{{Listed: label, Changed: id}})
				}
			}
		}
	}
}

func changedFileUnit(l *loaded, ch changes, label string) (string, string, bool) {
	path, ok := bazel.LabelPath(label)
	if !ok {
		return "", "", false
	}

	for _, t := range l.trees {
		if t == nil {
			continue
		}

		n, byUnits := t.ByUnits[path]
		if !byUnits {
			continue
		}

		for _, id := range t.FileUnits[path] {
			if i, ok := l.ix[id]; ok && ch.how[i] != "" {
				return n, id, true
			}
		}
	}

	return "", "", false
}
