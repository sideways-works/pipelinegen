package graph

import (
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
)

// The values of Change.How.
const (
	// HowDigest is a unit both trees have, with different digests.
	HowDigest = "digest"
	// HowAdded and HowRemoved are a unit only the head, or only the base,
	// has.
	HowAdded   = "added"
	HowRemoved = "removed"
	// HowLinks is a node one of whose links only one tree has.
	HowLinks = "links"
	// HowPin is a plugin's entry in the config that changed.
	HowPin = "pin"
	// HowUntold is the unit of a file an sbom couldn't tell about.
	HowUntold = "untold"
	// HowCannotTell is a unit with a group that can't be told in one tree
	// only.
	HowCannotTell = "cannot-tell"
)

const pinPrefix = "plugin:"

type changes struct {
	how                map[int32]string
	correlators        []string
	entryUnits, pinned map[int32]string
}

func changesOf(l *loaded, o Options, width string) changes {
	c := changes{how: map[int32]string{}, entryUnits: map[int32]string{}, pinned: map[int32]string{}}

	var sboms []string

	set := func(i int32, how string) {
		if _, ok := c.how[i]; !ok {
			c.how[i] = how
		}
	}

	untold := untoldUnits(l.trees)

	for i := range int32(len(l.ids)) { //nolint:gosec // see id
		b, inBase := l.digest[base][i]
		h, inHead := l.digest[head][i]

		how := ""

		switch {
		case inBase && inHead && b != h:
			how = HowDigest
		case inHead && !inBase:
			how = HowAdded
		case inBase && !inHead:
			how = HowRemoved
		}

		if how == "" {
			continue
		}

		id := l.ids[i]

		switch {
		case strings.HasPrefix(id, pinPrefix):
			if name, sbom := c.pin(o, id); sbom {
				sboms = append(sboms, name)
			}
		case untold[id]:
			set(i, HowUntold)
		default:
			set(i, how)
		}
	}

	for _, i := range toldInOneTree(l) {
		set(i, HowCannotTell)
	}

	for from, out := range l.out {
		for _, e := range out {
			if e.trees != 1<<base|1<<head && movedCounts(l, e, width) {
				set(int32(from), HowLinks)

				break
			}
		}
	}

	c.entries(l, sboms, on(o, narrowPluginEntry))

	sort.Strings(c.correlators)

	return c
}

// movedCounts tells whether a link in one tree only changes its from end in
// a width. A link to a unit that changed itself changes its from end no more
// than the unit does: the walk reaches every slice that follows the link
// through the unit.
func movedCounts(l *loaded, e edge, width string) bool {
	if width != Dependents && e.kind == dependentsOnly {
		return false
	}

	return !unitChanged(l, e.other)
}

func unitChanged(l *loaded, i int32) bool {
	b, inBase := l.digest[base][i]
	h, inHead := l.digest[head][i]

	return inBase != inHead || (inBase && b != h)
}

func toldInOneTree(l *loaded) []int32 {
	var out []int32

	for tr, t := range l.trees {
		other := l.trees[1-tr]
		if t == nil || other == nil {
			continue
		}

		for id := range t.CannotTell {
			i, ok := l.ix[id]
			if _, wide := other.CannotTell[id]; wide || !ok || l.group[i] == "" {
				continue
			}

			if _, in := l.digest[1-tr][i]; in {
				if _, in := l.digest[tr][i]; in {
					out = append(out, i)
				}
			}
		}
	}

	return out
}

func (c *changes) entries(l *loaded, sboms []string, entry bool) {
	for _, name := range sboms {
		for _, t := range l.trees {
			if t == nil {
				continue
			}

			for id, by := range t.Providers {
				i, ok := l.ix[id]
				if !ok || by != name {
					continue
				}

				c.entryUnits[i] = name

				if _, own := c.how[i]; own {
					continue
				}

				c.pinned[i] = name

				if !entry {
					c.how[i] = HowPin
				}
			}
		}
	}
}

func (c *changes) pin(o Options, id string) (string, bool) {
	name := strings.TrimPrefix(id, pinPrefix)

	p, ok := pluginNamed(o.Plugins, name)
	if !ok || !p.SBOM {
		c.correlators = append(c.correlators, name)

		return name, false
	}

	return name, true
}

func pluginNamed(ps []Plugin, name string) (Plugin, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}

	return Plugin{}, false
}

func untoldUnits(trees [2]*deps.Tree) map[string]bool {
	out := map[string]bool{}

	for _, t := range trees {
		if t == nil {
			continue
		}

		for _, files := range t.Untold {
			for _, f := range files {
				out[deps.FileID(f)] = true
			}
		}
	}

	return out
}

func (c *changes) list(l *loaded) []Change {
	out := make([]Change, 0, len(c.how)+len(c.correlators))

	for i, how := range c.how {
		out = append(out, Change{ID: l.ids[i], How: how})
	}

	for i := range c.pinned {
		if _, ok := c.how[i]; !ok {
			out = append(out, Change{ID: l.ids[i], How: HowPin})
		}
	}

	for _, name := range c.correlators {
		out = append(out, Change{ID: pinPrefix + name, How: HowPin})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}
