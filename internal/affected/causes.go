package affected

import (
	"cmp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
)

// Cause is a reason several selected slices of a suite share, named once
// with how many slices it selects.
type Cause struct {
	Kind    string   `json:"kind"`
	What    string   `json:"what"`
	Through []string `json:"through,omitempty"`
	Slices  int      `json:"slices"`
}

func causesOf(sp SuitePlan) []Cause {
	type key struct{ kind, what, through string }

	count := map[key]int{}
	first := map[key]Reason{}

	for _, sl := range sp.Selected {
		seen := map[key]bool{}

		for _, r := range sp.Reasons[sl.Label] {
			switch r.Kind {
			case KindNewSlice, KindTestAdded, KindTestRemoved, KindTestChanged:
				continue
			}

			// a walk's path runs back to the unit each slice recorded,
			// which differs from slice to slice: what changed is the cause
			if r.Kind == KindDepends || r.Kind == KindExecuted {
				r.Through = nil
			}

			k := key{r.Kind, r.What, strings.Join(r.Through, "\x00")}
			if seen[k] {
				continue
			}

			seen[k] = true
			count[k]++

			if _, ok := first[k]; !ok {
				first[k] = r
			}
		}
	}

	var out []Cause

	for k, n := range count {
		if n < 2 {
			continue
		}

		r := first[k]
		out = append(out, Cause{Kind: r.Kind, What: r.What, Through: r.Through, Slices: n})
	}

	slices.SortFunc(out, func(a, b Cause) int {
		return cmp.Or(b.Slices-a.Slices, strings.Compare(a.Kind, b.Kind), strings.Compare(a.What, b.What),
			strings.Compare(strings.Join(a.Through, "\x00"), strings.Join(b.Through, "\x00")))
	})

	return out
}

// Of tells whether r is one of the reasons the cause stands for.
func (c Cause) Of(r Reason) bool {
	if r.Kind != c.Kind || r.What != c.What {
		return false
	}

	return r.Kind == KindDepends || r.Kind == KindExecuted || slices.Equal(r.Through, c.Through)
}

// Text says a cause in a line: the file or unit that changed, and the
// label or units it reaches the slices through.
func (c Cause) Text() string {
	what := c.What
	if c.Kind == KindTarget {
		if p, ok := bazel.LabelPath(c.What); ok {
			what = p
		}
	}

	var through []string

	for _, t := range c.Through {
		if t != c.What && t != unexplained {
			through = append(through, t)
		}
	}

	text := what + " changed"

	switch c.Kind {
	case KindEverything, KindBaseConfig, KindConfig:
		return c.What
	case KindPlugin:
		text = "the entry of plugin " + c.What + " changed"
	case KindInputAdded:
		text = what + " is a new input"
	case KindInputRemoved:
		text = what + " is no longer an input"
	case KindInputUnknown:
		text = what + " can't be told unchanged"
	}

	if len(through) > 0 {
		text += ", through " + strings.Join(through, " > ")
	}

	if slices.Contains(c.Through, unexplained) {
		text += " (not explained in the plan)"
	}

	return text
}
