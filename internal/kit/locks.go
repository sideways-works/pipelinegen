package kit

import (
	"slices"
)

// lockSteps are the steps that run pipelinegen lock or unlock: those commands do nothing now, so
// such a step was written for an older tool.
func lockSteps(text []byte) []string {
	steps, err := stepCalls(text)
	if err != nil {
		return []string{err.Error()}
	}

	var out []string

	for _, c := range steps {
		if len(c.args) > 0 && (c.args[0] == "lock" || c.args[0] == "unlock") {
			out = appendNew(out, c.where+": runs pipelinegen "+c.args[0]+", and locks are gone")
		}
	}

	slices.Sort(out)

	return out
}
