package connector

import (
	"fmt"
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Admit tells whether a proposed change may run: only one known to come
// from the code's own repository. A fork's code, or code whose origin the
// connector couldn't tell, never gets a pipeline's credentials.
func Admit(c plugin.Change) bool { return c.From == plugin.FromSame }

// FilterProblems says, for each pull request filter a run sets that the
// connector has no capability for, what is missing; none when the
// connector can answer every one. A run whose connector can't tell drafts
// or labels apart would otherwise test changes it means to skip.
func FilterProblems(desc plugin.Description, filters []string) []string {
	var out []string

	for _, f := range filters {
		need, ok := plugin.FilterCapabilities[f]

		switch {
		case !ok:
			out = append(out, f+" is no pull request filter")
		case !slices.Contains(desc.Capabilities, need):
			out = append(out, fmt.Sprintf("%s needs the connector's %q capability, which %s doesn't declare", f, need, desc.Name))
		}
	}

	sort.Strings(out)

	return out
}
