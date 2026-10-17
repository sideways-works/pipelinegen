package check

import (
	"slices"

	"github.com/sideways-works/pipelinegen/internal/buildfile"
	"github.com/sideways-works/pipelinegen/internal/config"
)

// DeclaredCall is one call of a macro the config's macros declare, as the
// check reads it.
type DeclaredCall struct {
	Key      string
	Target   string
	Pkg      string
	Name     string
	Declared map[string][]string
	Named    []string
	Package  []string
}

// DeclaredCalls lists every call of a declared macro in a tree.
func DeclaredCalls(cfg *config.Config, tree Tree) []DeclaredCall {
	if len(cfg.Macros) == 0 {
		return nil
	}

	g := buildfile.Load(tree.Files, tree.Read)
	declared := declaredMacros(cfg)

	var out []DeclaredCall

	for _, label := range slices.Sorted(keys(g.Targets)) {
		t := g.Targets[label]

		key, ok := declared[t.Macro]
		if !ok {
			continue
		}

		c := DeclaredCall{Key: key, Target: label, Pkg: t.Pkg, Name: t.Name, Declared: map[string][]string{}}

		for _, p := range cfg.Macros[key].Reads {
			c.Declared[p] = paths(g, g.Glob(t.Pkg, []string{p}))
		}

		c.Named = paths(g, t.Inputs())
		c.Package = paths(g, g.Glob(t.Pkg, []string{"**"}))
		out = append(out, c)
	}

	return out
}

func declaredMacros(cfg *config.Config) map[string]string {
	out := map[string]string{}

	for key := range cfg.Macros {
		module, name, ok := config.MacroKey(key)
		if !ok {
			continue
		}

		if canon, ok := buildfile.Canonical("", module); ok {
			out[canon+"%"+name] = key
		}
	}

	return out
}

func paths(g *buildfile.Graph, labels []string) []string {
	var out []string

	for _, l := range labels {
		if p, ok := g.LabelFile(l); ok {
			out = append(out, p)
		}
	}

	return out
}
