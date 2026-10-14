package graph_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func budgetTree() graphtest.Pair {
	return graphtest.Generate(graphtest.Params{Seed: 1, Units: 3700, Links: 8, Slices: 100, Listed: 130, Changed: 5})
}

func quarterTree() graphtest.Pair {
	return graphtest.Generate(graphtest.Params{Seed: 1, Units: 925, Links: 8, Slices: 25, Listed: 130, Changed: 5})
}

// The walk's budget on budgetTree, in measures a busy machine doesn't
// change.
const (
	budgetAllocs = 250_000
	budgetMB     = 64
	// budgetGrowth bounds the work on four times the trees, as a multiple
	// of the work on one quarter: linear work is four times.
	budgetGrowth = 6
)

func walkCost(t *testing.T, pair graphtest.Pair) graphtest.Cost {
	t.Helper()

	var err error

	c := graphtest.Measure(func() {
		_, err = graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, graph.Options{})
	})

	if err != nil && strings.Contains(err.Error(), "not built yet") {
		t.Skip("the walk isn't built yet")
	}

	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestTheWalkKeepsItsBudget(t *testing.T) {
	pair, quarter := budgetTree(), quarterTree()

	c := walkCost(t, pair)

	t.Logf("the walk: %d allocations, %d MB, %v", c.Allocs, c.MB(), c.Took)

	if c.Allocs > budgetAllocs || c.MB() > budgetMB {
		t.Fatalf("the walk made %d allocations and held %d MB, over its budget of %d and %d MB", c.Allocs, c.MB(), budgetAllocs, budgetMB)
	}

	widths := []string{graph.Executed, graph.Dependents}
	small := graph.PlanSteps(quarter.Base, quarter.Head, quarter.Slices, widths, graph.Options{})
	large := graph.PlanSteps(pair.Base, pair.Head, pair.Slices, widths, graph.Options{})

	t.Logf("the walks' work: %d steps on the quarter, %d on the whole", small, large)

	if small == 0 || large > budgetGrowth*small {
		t.Fatalf("four times the trees took %d steps against %d: %.1f times, want at most %d", large, small, float64(large)/float64(max(small, 1)), budgetGrowth)
	}
}

func TestBindingForwardMissesTheBudget(t *testing.T) {
	if testing.Short() || raceOn {
		t.Skip("binds a hundred slices forward")
	}

	pair := budgetTree()

	var err error

	c := graphtest.Measure(func() { _, _, err = graphtest.Forward(t.Context(), &plugins.Set{}, pair.Base, pair.Head, pair.Slices) })

	if err != nil {
		t.Fatal(err)
	}

	t.Logf("binding forward: %d allocations, %d MB, %v", c.Allocs, c.MB(), c.Took)

	if c.Allocs <= budgetAllocs && c.MB() <= budgetMB {
		t.Fatalf("binding forward made %d allocations and held %d MB, within the walk's budget: the budget proves nothing", c.Allocs, c.MB())
	}
}

func TestTheWalkKeepsItsBudgetAtFullSize(t *testing.T) {
	if os.Getenv("PIPELINEGEN_WALK_SCALE") != "full" {
		t.Skip("PIPELINEGEN_WALK_SCALE=full runs it")
	}

	pair := graphtest.Generate(graphtest.Params{Seed: 1, Units: 37000, Links: 8, Slices: 1000, Listed: 1300, Changed: 20})

	c := walkCost(t, pair)

	t.Logf("the walk at full size: %v, %d MB", c.Took, c.MB())

	if c.MB() > 512 || (!raceOn && c.Took > time.Second) {
		t.Fatalf("the walk took %v and %d MB, over its budget of 1s and 512 MB", c.Took, c.MB())
	}
}
