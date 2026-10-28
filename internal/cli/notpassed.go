package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// Caps on what a step prints of the slices that did not pass: its log is often all a person sees.
const (
	whyTailLines = 15
	whyTailBytes = 1500
	whyNames     = 8
	whyBytes     = 6000
)

// whyNotPassed groups the slices that failed alike, capped; only, when set, names the slices to look at.
func whyNotPassed(ctx context.Context, store *gitstore.Store, id report.ID, suite string, only []string) string {
	files, err := report.StateFiles(ctx, store, id, suite)
	if err != nil {
		return "  (the slices' failures could not be read: " + err.Error() + ")\n"
	}

	groups := failedAlike(files, only)
	if len(groups) == 0 {
		return ""
	}

	text, cut := renderWhy(groups)

	where := "the full text of each: its state.json in the store, " + id.Prefix() + suite + "/<slice>/="
	if cut {
		where = "cut short here; " + where
	}

	return text + "  " + where + "\n"
}

type whyGroup struct {
	State    report.State     `json:"state"`
	Failures []report.Failure `json:"failures"`
	slices   []string
}

func failedAlike(files map[string]report.StateFile, only []string) []*whyGroup {
	names := only
	if names == nil {
		names = slices.Sorted(maps.Keys(files))
	}

	var groups []*whyGroup

	byKey := map[string]*whyGroup{}

	for _, name := range names {
		sf, ok := files[name]
		if !ok || sf.State == report.Passed {
			continue
		}

		g := &whyGroup{State: sf.State, Failures: sf.Failures}

		key, err := json.Marshal(g)
		if err != nil {
			continue
		}

		if seen, ok := byKey[string(key)]; ok {
			g = seen
		} else {
			byKey[string(key)] = g
			groups = append(groups, g)
		}

		g.slices = append(g.slices, name)
	}

	return groups
}

func renderWhy(groups []*whyGroup) (string, bool) {
	var b strings.Builder

	cut := false

	for _, g := range groups {
		if b.Len() > whyBytes {
			return b.String(), true
		}

		fmt.Fprintf(&b, "  %s, %s: %s\n", g.State, plural(len(g.slices), "slice"), sliceNames(g.slices))

		if len(g.Failures) == 0 {
			b.WriteString("    no failure recorded: the slice never said how it ended\n")
		}

		for _, f := range g.Failures {
			tail, short := capTail(f.Tail)
			cut = cut || short

			fmt.Fprintf(&b, "    %s\n", f.Test)

			for line := range strings.SplitSeq(tail, "\n") {
				if line != "" {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}

	return b.String(), cut
}

func sliceNames(names []string) string {
	if len(names) <= whyNames {
		return strings.Join(names, ", ")
	}

	return strings.Join(names[:whyNames], ", ") + fmt.Sprintf(" and %d more", len(names)-whyNames)
}

func capTail(tail string) (string, bool) {
	tail = strings.TrimRight(tail, "\n")
	lines := strings.Split(tail, "\n")
	cut := false

	if len(lines) > whyTailLines {
		lines, cut = lines[len(lines)-whyTailLines:], true
	}

	out := strings.Join(lines, "\n")
	if len(out) > whyTailBytes {
		out, cut = "..."+out[len(out)-whyTailBytes:], true
	}

	return out, cut
}

func withWhy(err error, why string) error {
	if why == "" {
		return err
	}

	return fmt.Errorf("%w\n%s", err, strings.TrimRight(why, "\n"))
}
