package affected

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

func reasons(ctx context.Context, o Options, b, h *Snapshot, suite string, selected []bazel.Slice, changes []gitx.Change) map[string][]Reason {
	before := map[string]map[string]bool{}

	for _, sl := range b.Slices[suite] {
		before[sl.Label] = set(sl.Sources)
	}

	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.Path] = true
	}

	out := map[string][]Reason{}

	for _, sl := range selected {
		out[sl.Label] = sliceReasons(ctx, o, b, h, sl, selectorFor(sl.Label, o.width(suite)), before, changed)
	}

	return out
}

func sliceReasons(ctx context.Context, o Options, b, h *Snapshot, sl bazel.Slice, target string, before map[string]map[string]bool, changed map[string]bool) []Reason {
	old, existed := before[sl.Label]
	if !existed {
		return []Reason{{Kind: KindNewSlice, What: sl.Label}}
	}

	rs := testReasons(sl, old, changed)

	// what Prepare wrote may tell why on its own, without bazel-diff
	if o.Reasons != nil {
		if more, ok := o.Reasons(b.Commit, h.Commit, target); ok {
			return append(rs, more...)
		}
	}

	if budget := o.explained; budget != nil && budget.limited {
		if budget.left <= 0 {
			budget.skipped++

			return append(rs, Reason{Kind: KindTarget, What: target, Through: []string{unexplained}})
		}

		budget.left--
	}

	rs = causeReasons(ctx, o, b, h, sl, target, rs)

	if len(rs) == 0 {
		rs = append(rs, Reason{Kind: KindTarget, What: sl.Label})
	}

	return rs
}

func testReasons(sl bazel.Slice, old, changed map[string]bool) []Reason {
	var rs []Reason

	now := set(sl.Sources)

	for _, src := range sl.Sources {
		switch {
		case !old[src]:
			rs = append(rs, Reason{Kind: KindTestAdded, What: src})
		case changed[src]:
			rs = append(rs, Reason{Kind: KindTestChanged, What: src})
		}
	}

	for src := range old {
		if !now[src] {
			rs = append(rs, Reason{Kind: KindTestRemoved, What: src})
		}
	}

	return rs
}

func causeReasons(ctx context.Context, o Options, b, h *Snapshot, sl bazel.Slice, target string, rs []Reason) []Reason {
	causes, err := o.Differ.Explain(ctx, b.Hashes, h.Hashes, h.Edges, target)
	if err != nil {
		return append(rs, Reason{Kind: KindTarget, What: "bazel-diff could not explain: " + err.Error()})
	}

	for i, c := range causes {
		if i == maxRootCauses {
			break
		}

		// what Prepare wrote may name the unit behind a cause, the
		// selector itself included
		if o.Why != nil {
			if more, ok := o.Why(b.Commit, h.Commit, target, c); ok {
				// two root causes may come down to the same unit
				for _, r := range more {
					if !slices.ContainsFunc(rs, func(have Reason) bool { return have.Kind == r.Kind && have.What == r.What }) {
						rs = append(rs, r)
					}
				}

				continue
			}
		}

		if isOwnSource(c.Label, sl) {
			continue
		}

		rs = appendNew(rs, o.causeTargets(ctx, c)...)
	}

	return rs
}

// causeTargets are the reasons a root cause stands for: its own changed files when Bazel can name them,
// else the cause; bazel-diff hashes a rule's files into the rule, so the rule is its deepest cause.
func (o Options) causeTargets(ctx context.Context, c RootCause) []Reason {
	files := o.quiet.observable(o.Config, o.filesBehind(ctx, c.Label))
	if len(files) == 0 {
		return []Reason{{Kind: KindTarget, What: c.Label, Through: c.Path}}
	}

	out := make([]Reason, 0, len(files))
	for _, f := range files {
		out = append(out, Reason{Kind: KindTarget, What: f, Through: c.Path})
	}

	return out
}

func (o Options) filesBehind(ctx context.Context, rule string) []string {
	memo := o.explained
	if memo == nil || memo.files == nil || o.Bazel == nil || o.Config == nil || len(memo.changed) == 0 || !strings.HasPrefix(rule, "//") {
		return nil
	}

	if files, ok := memo.files[rule]; ok {
		return files
	}

	memo.files[rule] = nil

	out, err := o.Bazel(workspaceDir(o)).Run(ctx, "query", `kind("source file", deps(`+rule+`, 1))`, "--output=label", "--keep_going=false")
	if err != nil {
		return nil
	}

	var files []string

	for line := range strings.SplitSeq(string(out), "\n") {
		label := strings.TrimSpace(line)

		p, ok := bazel.LabelPath(label)
		if !ok || !memo.changed[path.Join(o.Config.Bazel.Workspace, p)] {
			continue
		}

		files = append(files, bazel.NormalLabel(label))
	}

	slices.Sort(files)
	memo.files[rule] = slices.Compact(files)

	return memo.files[rule]
}

const unexplained = "not explained in the plan: pipelinegen why --slice explains it"

func isOwnSource(label string, sl bazel.Slice) bool {
	if label == sl.Label || label == sl.Label+dependentsSuffix {
		return true
	}

	p, ok := bazel.LabelPath(label)

	return ok && set(sl.Sources)[p] || strings.HasSuffix(label, ":BUILD.bazel") || strings.HasSuffix(label, ":BUILD")
}

func set(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}

	return m
}
