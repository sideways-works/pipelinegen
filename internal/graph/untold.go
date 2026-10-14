package graph

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// untoldSBOM is an sbom that couldn't tell about files in a tree, one of
// whose file units changed: a recorded slice that reaches its namespaces or
// owns one of its files depends on those files.
type untoldSBOM struct {
	name  string
	files []string
	// starts are its namespaces' units in either tree; all marks an sbom
	// without namespaces, whose units could be anything.
	starts []int32
	all    bool
}

type untoldWalk struct {
	untoldSBOM

	w          *walk
	dependents bool
}

func untoldSBOMs(l *loaded, ch changes, o Options) []untoldSBOM {
	files := map[string][]string{}

	for _, t := range l.trees {
		if t == nil {
			continue
		}

		for name, fs := range t.Untold {
			for _, f := range fs {
				if !slices.Contains(files[name], f) {
					files[name] = append(files[name], f)
				}
			}
		}
	}

	var out []untoldSBOM

	for _, name := range sortedKeys(files) {
		p, ok := pluginNamed(o.Plugins, name)
		if !ok || !p.SBOM || !anyChanged(l, ch, files[name]) {
			continue
		}

		u := untoldSBOM{name: name, files: files[name], all: len(p.Namespaces) == 0}

		u.starts = unitsIn(l, p.Namespaces)

		out = append(out, u)
	}

	return out
}

func unitsIn(l *loaded, namespaces []string) []int32 {
	var out []int32

	for _, tr := range []int{base, head} {
		for i := range l.digest[tr] {
			for _, ns := range namespaces {
				if strings.HasPrefix(l.ids[i], ns+":") {
					out = append(out, i)

					break
				}
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func anyChanged(l *loaded, ch changes, files []string) bool {
	for _, f := range files {
		if i, ok := l.ix["file:"+f]; ok {
			if _, changed := ch.how[i]; changed {
				return true
			}
		}
	}

	return false
}

func (u untoldSBOM) in(l *loaded, dependents bool) *untoldWalk {
	sources := make([]source, 0, len(u.starts))
	for _, i := range u.starts {
		sources = append(sources, source{at: i, node: i})
	}

	return &untoldWalk{untoldSBOM: u, w: walkFrom(l, dependents, sources), dependents: dependents}
}

func (u *untoldWalk) selects(l *loaded, sl plugins.BindSlice, roots []int32) (Path, bool) {
	p := Path{Changed: "file:" + u.files[0], Steps: []Step{{From: pinPrefix + u.name, To: "file:" + u.files[0], Kind: KindUntold}}}

	if u.all {
		return p, true
	}

	for _, src := range sl.Sources {
		if slices.Contains(u.files, src) {
			p.Listed = "file:" + src

			return p, true
		}
	}

	for _, r := range roots {
		if c, _ := u.w.reached(r); c >= 0 && (!u.dependents || sl.Depth == 0 || int(c) <= sl.Depth) {
			p.Listed = l.ids[r]

			return p, true
		}
	}

	return Path{}, false
}
