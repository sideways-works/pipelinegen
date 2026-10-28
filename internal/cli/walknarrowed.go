package cli

import (
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/graph"
)

// A slice is named under every narrowing whose walk alone selects it, as the book's records name it.
func walkNarrowedHook(sel *graph.Selection, offAt func(narrowing string) int) func(base, head, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
	if sel == nil {
		return nil
	}

	return func(_, _, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
		return walkNarrowed(sel, offAt, suite, width, selected, left)
	}
}

func walkNarrowed(sel *graph.Selection, offAt func(string) int, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
	hits := map[string][]narrowHit{}

	for _, sl := range left {
		key := graph.Key{Suite: suite, Name: sl.Name, Width: width}

		for _, n := range sortedKeys(sel.LeftOut) {
			if paths, ok := sel.LeftOut[n][key]; ok {
				hits[sl.Name] = append(hits[sl.Name], narrowHit{n, leftOutWhat(paths)})
			}
		}
	}

	return namedLeftOut(hits, offAt, len(selected))
}

func entryWhat(name string) string { return "the entry of plugin " + name }

func leftOutWhat(paths []graph.Path) string {
	if len(paths) == 0 {
		return "a unit"
	}

	p := paths[0]
	if p.Entry != "" {
		return entryWhat(p.Entry)
	}

	if name, ok := strings.CutPrefix(p.Changed, pinPrefix); ok {
		return entryWhat(name)
	}

	if n := len(p.Steps); n > 0 && p.Steps[n-1].Kind == graph.KindNames {
		return "what nothing binds in " + strings.TrimPrefix(p.Changed, graph.GroupPrefix)
	}

	return p.Changed
}
