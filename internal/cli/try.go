package cli

import (
	"bytes"
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

// runTry is how a repository sees what the tool would have run before it adopts it.
func runTry(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c    common
		last int
		head string
		lp   localPlan
	)

	fs := flag.NewFlagSet("try", flag.ContinueOnError)
	c.register(fs)
	fs.IntVar(&last, "last", 10, "how many first-parent commits to plan, newest first")
	textVar(fs, &head, "head", "HEAD", "the newest commit to plan")
	lp.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	ctx = withFill(ctx, "try", *lp.noFill)

	if last < 1 {
		return fmt.Errorf("%w: --last must be at least 1", errUsage)
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	root, err := filepath.Abs(c.root)
	if err != nil {
		return fmt.Errorf("root: %w", err)
	}

	lp.cache = repoCache(ctx, lp.cache, root)

	if lp.plugins == "" {
		lp.plugins = filepath.Join(root, "tools", "plugins")
	}

	out, err := gitx.Repo{Dir: root}.Git(ctx, "rev-list", "--first-parent", "--parents", "-n", strconv.Itoa(last), head, "--")
	if err != nil {
		return fmt.Errorf("try: %w", err)
	}

	var (
		b     bytes.Buffer
		tally tryTally
	)

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		ids := strings.Fields(line)
		if len(ids) < 2 {
			continue
		}

		p, err := affectedPlanUnder(ctx, cfg, root, ids[1], ids[0], false, lp.cache, lp.bazelDiff, listsNone, absOr(lp.plugins), true)
		if err != nil {
			return fmt.Errorf("try %s: %w", short12(ids[0]), err)
		}

		fmt.Fprintln(&b, tally.add(ids[0], p))
	}

	fmt.Fprintln(&b, tally.String())

	if line := tally.lacking(); line != "" {
		fmt.Fprintln(&b, line)
	}

	return emit(stdout, &b)
}

type tryTally struct {
	commits, everything int
	shares              map[string][]float64 // per suite, each plan's share selected
	reasons             map[string]int       // why a plan selected everything
	lacked              map[string]int
	lackingPlans        int
}

func (t *tryTally) add(commit string, p *affected.Plan) string {
	if t.shares == nil {
		t.shares, t.reasons = map[string][]float64{}, map[string]int{}
	}

	t.commits++
	t.addLacked(p)

	parts := make([]string, 0, 1+len(p.Suites))
	parts = append(parts, short12(commit))

	for _, sp := range p.Suites {
		total := len(sp.Selected) + len(sp.Untouched)
		parts = append(parts, fmt.Sprintf("%s %d/%d", sp.Suite, len(sp.Selected), total))

		if total > 0 {
			t.shares[sp.Suite] = append(t.shares[sp.Suite], float64(len(sp.Selected))/float64(total))
		}
	}

	if p.All {
		t.everything++
		t.reasons[firstClause(p.Reason)]++

		return strings.Join(parts, "  ") + "  everything: " + p.Reason
	}

	return strings.Join(parts, "  ") + "  " + firstReason(p)
}

// String sums up the plans: per suite the median and worst share selected, and the commonest reasons
// for selecting everything.
func (t *tryTally) String() string {
	parts := []string{fmt.Sprintf("%d commits", t.commits)}

	for _, suite := range slices.Sorted(maps.Keys(t.shares)) {
		s := slices.Sorted(slices.Values(t.shares[suite]))
		parts = append(parts, fmt.Sprintf("%s median %.0f%% worst %.0f%%", suite, 100*s[(len(s)-1)/2], 100*s[len(s)-1]))
	}

	everything := fmt.Sprintf("%d ran everything", t.everything)

	if t.everything > 0 {
		reasons := slices.Collect(maps.Keys(t.reasons))
		slices.SortFunc(reasons, func(a, b string) int {
			return cmp.Or(cmp.Compare(t.reasons[b], t.reasons[a]), strings.Compare(a, b))
		})

		top := make([]string, 0, min(len(reasons), 3))
		for _, r := range reasons[:min(len(reasons), 3)] {
			top = append(top, fmt.Sprintf("%s (%d)", r, t.reasons[r]))
		}

		everything += ": " + strings.Join(top, "; ")
	}

	return "total: " + strings.Join(append(parts, everything), ", ")
}

// firstClause cuts a reason at its first colon or parenthesis, so the same kind with different files
// counts as one.
func firstClause(reason string) string {
	if i := strings.IndexAny(reason, ":("); i > 0 {
		return strings.TrimSpace(reason[:i])
	}

	return reason
}

func firstReason(p *affected.Plan) string {
	for _, sp := range p.Suites {
		for _, sl := range sp.Selected {
			if rs := sp.Reasons[sl.Label]; len(rs) > 0 {
				return shownName(sl) + ": " + reasonLine(rs[0])
			}
		}
	}

	return "nothing selected"
}

// A config written for today's tree often names targets the conversion's own BUILD changes added.
func (t *tryTally) addLacked(p *affected.Plan) {
	seen := map[string]bool{}

	for _, sp := range p.Suites {
		for _, rs := range sp.Reasons {
			for _, r := range rs {
				if r.Kind == affected.KindInputUnknown && len(r.Through) == 0 {
					seen[r.What] = true
				}
			}
		}
	}

	if len(seen) == 0 {
		return
	}

	if t.lacked == nil {
		t.lacked = map[string]int{}
	}

	t.lackingPlans++

	for l := range seen {
		t.lacked[l]++
	}
}

func (t *tryTally) lacking() string {
	if t.lackingPlans == 0 {
		return ""
	}

	labels := slices.Sorted(maps.Keys(t.lacked))

	return fmt.Sprintf("%d of %d commits depend on targets their trees lack or bazel-diff doesn't hash: %s; "+
		"a config names targets the conversion's own BUILD changes add, so the commits before them run those slices: "+
		"replay the commits onto a branch that has the BUILD changes and try that branch (see try in the commands reference)",
		t.lackingPlans, t.commits, strings.Join(labels, ", "))
}
