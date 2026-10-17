package affected

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/check"
)

// ErrDeclared is a macros declaration Bazel doesn't confirm: the check
// counts files as covered by a call that doesn't read them.
var ErrDeclared = errors.New("a macros declaration claims files the macro doesn't read")

// MacroCheck is what Bazel says of the commit's macros declarations: each
// file a call is declared to read but doesn't (an error), and each file of
// the call's package it reads that the declaration leaves out (a warning).
type MacroCheck struct {
	Unconfirmed []string `json:"unconfirmed,omitempty"`
	LeftOut     []string `json:"left_out,omitempty"`
}

func headSnapshot(ctx context.Context, o Options, head string) (*Snapshot, error) {
	h, err := snapshot(ctx, o, head)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHead, err)
	}

	if m := h.Macros; m != nil {
		if len(m.Unconfirmed) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrDeclared, strings.Join(m.Unconfirmed, "; "))
		}

		for _, w := range m.LeftOut {
			o.warnf("%s", w)
		}
	}

	return h, nil
}

const maxListed = 10

func checkMacros(ctx context.Context, o Options, bz *bazel.Client, commit string) (*MacroCheck, error) {
	if len(o.Config.Macros) == 0 {
		return nil, nil //nolint:nilnil // no declaration, nothing to say
	}

	tree, closeTree, err := check.CommitTree(ctx, o.Repo.Dir, commit)
	if err != nil {
		return nil, fmt.Errorf("macros: %w", err)
	}

	defer closeTree() //nolint:errcheck // a reader of git objects; what it read is done

	out := &MacroCheck{}

	for _, c := range check.DeclaredCalls(o.Config, tree) {
		read, err := macroReads(ctx, bz, o.Config.Bazel.Workspace, c)
		if err != nil {
			return nil, err
		}

		out.add(c, read)
	}

	return out, nil
}

// macroReads are the tree's source files the targets a call made depend on:
// those its macro marked with the call's name (a legacy macro's
// generator_name) or named after it (a symbolic macro's targets).
func macroReads(ctx context.Context, bz *bazel.Client, workspace string, c check.DeclaredCall) (map[string]bool, error) {
	pkg := "//" + c.Pkg
	name := regexp.QuoteMeta(c.Name)
	made := fmt.Sprintf(`attr(generator_name, "^%s$", %s:*) + filter(":%s([_.-][^:]*)?$", kind(rule, %s:*))`,
		name, pkg, name, pkg)

	q, err := bz.Run(ctx, "query", `kind("source file", deps(`+made+`))`, "--output=label", "--keep_going=false")
	if err != nil {
		return nil, fmt.Errorf("macros %q: what %s reads: %w", c.Key, c.Target, err)
	}

	read := map[string]bool{}

	for line := range strings.SplitSeq(string(q), "\n") {
		if p, ok := bazel.LabelPath(strings.TrimSpace(line)); ok {
			read[path.Join(workspace, p)] = true
		}
	}

	return read, nil
}

func (m *MacroCheck) add(c check.DeclaredCall, read map[string]bool) {
	declared := map[string]bool{}

	for _, p := range slices.Sorted(mapKeys(c.Declared)) {
		var missing []string

		for _, f := range c.Declared[p] {
			declared[f] = true

			if !read[f] {
				missing = append(missing, f)
			}
		}

		if len(missing) > 0 {
			m.Unconfirmed = append(m.Unconfirmed, fmt.Sprintf("macros %q: reads %q claims %s for %s, and Bazel says nothing %s made reads them; the check counts them as covered, so a change to them may run nothing",
				c.Key, p, listed(missing), c.Target, c.Target))
		}
	}

	var left []string

	for _, f := range c.Package {
		if read[f] && !declared[f] && !slices.Contains(c.Named, f) {
			left = append(left, f)
		}
	}

	if len(left) > 0 {
		m.LeftOut = append(m.LeftOut, fmt.Sprintf("macros %q: Bazel says %s reads %s, which neither the declaration nor the call names; the check calls them uncovered unless another slice covers them",
			c.Key, c.Target, listed(left)))
	}
}

func listed(files []string) string {
	if len(files) <= maxListed {
		return strings.Join(files, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(files[:maxListed], ", "), len(files)-maxListed)
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
