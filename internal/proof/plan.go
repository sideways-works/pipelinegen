package proof

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

type planJSON struct {
	Reason string `json:"reason"`
	Suites []struct {
		Selected []struct {
			Name string `json:"name"`
		} `json:"selected"`
		Untouched []string `json:"untouched"`
		Reasons   map[string][]struct {
			Kind string `json:"kind"`
			What string `json:"what"`
		} `json:"reasons"`
		Narrowed []struct {
			Kind      string   `json:"kind"`
			Narrowing string   `json:"narrowing"`
			OffAt     int      `json:"off_at"`
			Dropped   []string `json:"dropped"`
		} `json:"narrowed"`
	} `json:"suites"`
	Plugins *struct {
		Warnings []string `json:"warnings"`
	} `json:"plugins"`
}

const depsPrefix = "@pipelinegen_deps//slices:"

// Plan is what the proof reads of one plan: the selected and untouched slices, the reasons, what the
// narrowings left out, and the warnings.
type Plan struct {
	p                             planJSON
	selected, untouched, warnings []string
}

// ReadPlan reads a plan's JSON, as affected --json writes it.
func ReadPlan(file string) (*Plan, error) {
	data, err := os.ReadFile(file) //nolint:gosec // a plan the proof wrote
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	var p Plan
	if err := json.Unmarshal(data, &p.p); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}

	for _, s := range p.p.Suites {
		for _, sl := range s.Selected {
			p.selected = append(p.selected, sl.Name)
		}

		for _, u := range s.Untouched {
			p.untouched = append(p.untouched, strings.TrimPrefix(u, depsPrefix))
		}
	}

	if p.p.Plugins != nil {
		p.warnings = p.p.Plugins.Warnings
	}

	return &p, nil
}

// Selected are the selected slices, sorted, comma-separated.
func (p *Plan) Selected() string {
	return strings.Join(slices.Sorted(slices.Values(p.selected)), ",")
}

// Shown are the selected slices as a table's cell shows them.
func (p *Plan) Shown() string {
	if len(p.selected) == 0 {
		return nothing
	}

	return strings.Join(p.selected, ", ")
}

func (p *Plan) FirstSelected() string { return first(p.selected) }

func (p *Plan) FirstUntouched() string { return first(p.untouched) }

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}

	return list[0]
}

func (p *Plan) Count() int { return len(p.selected) }

func (p *Plan) Total() int { return len(p.selected) + len(p.untouched) }

// Sampled is how many entries the narrowing sampled.
func (p *Plan) Sampled(narrowing string) int {
	n := 0

	for _, s := range p.p.Suites {
		for _, e := range s.Narrowed {
			if e.Kind == "sampled" && e.Narrowing == narrowing {
				n++
			}
		}
	}

	return n
}

// Named is how many slices the narrowing's entries name.
func (p *Plan) Named(narrowing string) int {
	n := 0

	for _, s := range p.p.Suites {
		for _, e := range s.Narrowed {
			if e.Narrowing == narrowing {
				n += len(e.Dropped)
			}
		}
	}

	return n
}

// Narrowing is one entry of what a plan's narrowings left out.
type Narrowing struct {
	Name    string
	OffAt   int
	Dropped string
}

// Narrowed are the plan's narrowings, each with the level that switches it
// off and what it left out, sorted and comma-separated.
func (p *Plan) Narrowed() []Narrowing {
	var out []Narrowing

	for _, s := range p.p.Suites {
		for _, e := range s.Narrowed {
			out = append(out, Narrowing{Name: e.Narrowing, OffAt: e.OffAt, Dropped: strings.Join(slices.Sorted(slices.Values(e.Dropped)), ",")})
		}
	}

	return out
}

// Line is the entry as one tab-separated line.
func (n Narrowing) Line() string { return fmt.Sprintf("%s\t%d\t%s", n.Name, n.OffAt, n.Dropped) }

// NarrowedShown is what the narrowings did, as a table's cell shows it.
func (p *Plan) NarrowedShown() string {
	var out []string

	for _, s := range p.p.Suites {
		for _, e := range s.Narrowed {
			out = append(out, fmt.Sprintf("%s %s (left out %d)", e.Kind, e.Narrowing, len(e.Dropped)))
		}
	}

	if len(out) == 0 {
		return "-"
	}

	return strings.Join(out, "; ")
}

func (p *Plan) Warnings() []string { return p.warnings }

// Warned tells whether a warning names the slice.
func (p *Plan) Warned(slice string) bool {
	return slices.ContainsFunc(p.warnings, func(w string) bool { return strings.Contains(w, slice) })
}

// Reason is why the plan selected everything, if it did.
func (p *Plan) Reason() string { return p.p.Reason }

func (p *Plan) Reasons(slice string) []string {
	var out []string

	for _, s := range p.p.Suites {
		for _, r := range s.Reasons[depsPrefix+slice] {
			out = append(out, r.What)
		}
	}

	return out
}

func (p *Plan) HasReasonKind(slice, kind string) bool {
	for _, s := range p.p.Suites {
		for _, r := range s.Reasons[depsPrefix+slice] {
			if r.Kind == kind {
				return true
			}
		}
	}

	return false
}
