package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Rebuild is what the nightly's cold rebuild of the head's graph found:
// every answer that differs from what the cache serves, and the error that
// kept the rebuild from finishing.
type Rebuild struct {
	Check       string              `json:"check"`
	Differences []RebuildDifference `json:"differences,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// RebuildDifference is one answer that differs: the plugin and call, the
// file or set it answers for, and what differs, one line each.
type RebuildDifference struct {
	Plugin     string   `json:"plugin"`
	Call       string   `json:"call"`
	Inputs     []string `json:"inputs"`
	InputUnits []string `json:"input_units,omitempty"`
	What       []string `json:"what"`
}

// Faults are how many things the rebuild found wrong: each differing
// answer, and a rebuild that couldn't finish.
func (r *Rebuild) Faults() int {
	if r == nil {
		return 0
	}

	n := len(r.Differences)
	if r.Error != "" {
		n++
	}

	return n
}

func (r *Rebuild) failing() bool {
	return r.Faults() > 0 && r.Check != config.RebuildWarning
}

// Lines say what the rebuild found, one line per fault, masked.
func (r *Rebuild) Lines() []string {
	if r == nil {
		return nil
	}

	out := make([]string, 0, r.Faults())

	if r.Error != "" {
		out = append(out, secret.Redact("the cold rebuild of the graph could not finish: "+r.Error))
	}

	for _, d := range r.Differences {
		of := "set " + strings.Join(d.Inputs, ", ")
		if len(d.Inputs) == 1 {
			of = "file " + d.Inputs[0]
		}

		if len(d.InputUnits) > 0 {
			of += " with units " + strings.Join(d.InputUnits, ", ")
		}

		out = append(out, secret.Redact(fmt.Sprintf("the cold rebuild differs from the cache: %s %s, %s: %s", d.Plugin, d.Call, of, strings.Join(d.What, "; "))))
	}

	return out
}

func (r *Rebuild) plugins() []string {
	var out []string

	for _, d := range r.Differences {
		if !slices.Contains(out, d.Plugin) {
			out = append(out, d.Plugin)
		}
	}

	slices.Sort(out)

	return out
}

func (r *Rebuild) summary() string {
	if len(r.Differences) == 0 {
		return "the cold rebuild of the graph could not finish"
	}

	return fmt.Sprintf("the cold rebuild differs from the cache in %s (%s)", plural(len(r.Differences), "answer"), strings.Join(r.plugins(), ", "))
}
