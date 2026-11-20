package forgeplugin

import (
	"fmt"
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Admit tells whether a proposed change may run: only one known to come from the code's own repository;
// a fork's code, or code of an origin the forge couldn't tell, never gets a pipeline's credentials.
func Admit(c plugin.Change) bool { return c.From == plugin.FromSame }

// FilterProblems names, per pull request filter the run sets, the capability the forge lacks for it:
// one that can't tell drafts or labels apart would otherwise test changes the run means to skip.
func FilterProblems(desc plugin.Description, filters []string) []string {
	var out []string

	for _, f := range filters {
		need, ok := plugin.FilterCapabilities[f]

		switch {
		case !ok:
			out = append(out, f+" is no pull request filter")
		case !slices.Contains(desc.Capabilities, need):
			out = append(out, fmt.Sprintf("%s needs the forge's %q capability, which %s doesn't declare", f, need, desc.Name))
		}
	}

	sort.Strings(out)

	return out
}
