package cli

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// costTop is how many changed units and junctions `why --cost` lists per suite.
const costTop = 10

// Cost is what a plan's selection cost in one suite: the changed units that selected its slices, and
// the units and junctions on the way there, each with the slices it led to and those it alone led to.
type Cost struct {
	Suite    string      `json:"suite"`
	Selected int         `json:"selected"`
	Of       int         `json:"of"`
	All      bool        `json:"all,omitempty"`
	Reason   string      `json:"reason,omitempty"`
	Changed  []CostEntry `json:"changed"`
	Through  []CostEntry `json:"through"`
}

// CostEntry is one unit or junction: the selected slices a reason of which names it, and those all of
// whose reasons do, which a narrowing there would leave out.
type CostEntry struct {
	What   string `json:"what"`
	Slices int    `json:"slices"`
	Alone  int    `json:"alone"`
}

// costs are the plan's costs, per suite it selected something in; suite, when set, is the one.
func costs(p *whyPlan, suite string) []Cost {
	var out []Cost

	for _, s := range p.Suites {
		if (suite != "" && s.Suite != suite) || len(s.Selected) == 0 {
			continue
		}

		out = append(out, suiteCost(s))
	}

	return out
}

func suiteCost(s whySuite) Cost {
	c := Cost{Suite: s.Suite, Selected: len(s.Selected), Of: len(s.Selected) + len(s.Untouched), All: s.All, Reason: s.Reason}

	changed, through := costCounter{}, costCounter{}

	for _, sel := range s.Selected {
		rs := s.Reasons[sel.Label]
		if len(rs) == 0 {
			continue
		}

		changed.slice(rs, func(r affected.Reason) []string { return []string{r.What} })
		// a slice's own unit ends each path: it leads to that slice alone, so it costs nothing to keep
		through.slice(rs, func(r affected.Reason) []string { return r.Through[:max(len(r.Through)-1, 0)] })
	}

	c.Changed, c.Through = changed.top(1), through.top(2)

	return c
}

// costCounter counts per unit the slices any reason names it in, and those every reason does.
type costCounter map[string]*CostEntry

func (cc costCounter) slice(rs []affected.Reason, units func(affected.Reason) []string) {
	named := map[string]bool{}
	every := map[string]int{}

	for _, r := range rs {
		seen := map[string]bool{}

		for _, u := range units(r) {
			if u == "" || seen[u] {
				continue
			}

			seen[u] = true
			named[u] = true
			every[u]++
		}
	}

	for u := range named {
		e := cc[u]
		if e == nil {
			e = &CostEntry{What: u}
			cc[u] = e
		}

		e.Slices++

		if every[u] == len(rs) {
			e.Alone++
		}
	}
}

// top are the entries leading to fewest slices or more, those leading to the most alone first, then
// to the most.
func (cc costCounter) top(fewest int) []CostEntry {
	out := make([]CostEntry, 0, len(cc))

	for _, e := range cc {
		if e.Slices >= fewest {
			out = append(out, *e)
		}
	}

	slices.SortFunc(out, func(a, b CostEntry) int {
		return cmp.Or(cmp.Compare(b.Alone, a.Alone), cmp.Compare(b.Slices, a.Slices), strings.Compare(a.What, b.What))
	})

	return out[:min(len(out), costTop)]
}

func writeCosts(stdout io.Writer, cs []Cost, title string, asJSON bool, warnings []string) error {
	var b bytes.Buffer

	if asJSON {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(cs); err != nil {
			return fmt.Errorf("encode: %w", err)
		}

		return emit(stdout, &b)
	}

	if len(cs) == 0 {
		fmt.Fprintf(&b, "nothing was selected %s\n", title)
	}

	for _, c := range cs {
		fmt.Fprintf(&b, "%s: %d of %d selected %s", c.Suite, c.Selected, c.Of, title)

		if c.All && c.Reason != affected.ReachesEverySlice && c.Reason != "" {
			fmt.Fprintf(&b, ", the whole suite: %s", c.Reason)
		}

		b.WriteString("\n")
		costLines(&b, "changed, by the slices it selects (alone: the slices nothing else selects)", c.Changed)
		costLines(&b, "on the way, by the slices that reach a change only through it", c.Through)
	}

	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", secret.Redact(w))
	}

	return emit(stdout, &b)
}

func costLines(b *bytes.Buffer, head string, es []CostEntry) {
	if len(es) == 0 {
		return
	}

	fmt.Fprintf(b, "  %s:\n", head)

	width := 0
	for _, e := range es {
		width = max(width, len(e.What))
	}

	for _, e := range es {
		fmt.Fprintf(b, "    %-*s  %d (%d alone)\n", width, e.What, e.Slices, e.Alone)
	}
}
