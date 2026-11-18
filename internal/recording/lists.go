package recording

import "slices"

// ID is a unit id as the lists keep it: opaque to everything but the
// plugin that named it.
type ID string

// Lists are recorded lists by slice key, `<suite>/<slice>`.
type Lists map[string][]ID

// SetupList is the name, within a suite, of the list of what its batches'
// setup ran: `<suite>/_setup` among the lists.
const SetupList = "_setup"

// SetupTarget is the target in the deps repository bound to what a suite's
// setup ran, when the setup split is on.
func SetupTarget(suite string) string {
	return SetupList + "." + suite
}

// Merge folds one run's recordings into the lists, which only grow, except that a slice recorded in a
// run that selected everything takes its recording as its whole list.
func Merge(lists, recordings Lists, selectedAll bool) Lists {
	out := make(Lists, len(lists)+len(recordings))

	for k, ids := range lists {
		out[k] = slices.Clone(ids)
	}

	for k, ids := range recordings {
		if selectedAll {
			out[k] = slices.Clone(ids)
		} else {
			out[k] = append(out[k], ids...)
		}
	}

	for k, ids := range out {
		slices.Sort(ids)
		out[k] = slices.Compact(ids)
	}

	return out
}

// Union is the lists of several sources together (the compared commit's
// lists and the recordings of runs still going).
func Union(sources ...Lists) Lists {
	out := Lists{}

	for _, src := range sources {
		for k, ids := range src {
			out[k] = append(out[k], ids...)
		}
	}

	for k, ids := range out {
		slices.Sort(ids)
		out[k] = slices.Compact(ids)
	}

	return out
}
