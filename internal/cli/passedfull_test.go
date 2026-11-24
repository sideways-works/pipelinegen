package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// newFullPassedRepo is newPassedRepo with its run made a full run that keeps what passed within a day.
func newFullPassedRepo(t *testing.T) *roundsRepo {
	t.Helper()

	w := newPassedRepo(t)
	conf := readFile(t, filepath.Join(w.r.Dir, config.FileName))

	full := strings.Replace(conf, "    compare: last_passed\n    select: affected\n", "    compare: none\n    select: all\n    passed_within: 24h\n", 1)
	if full == conf {
		t.Fatalf("the run's compare and select changed in the fixture:\n%s", conf)
	}

	w.r.Write(config.FileName, full)

	return w
}

func TestAFullRunKeepsWhatPassedWithinItsWindow(t *testing.T) {
	w := newFullPassedRepo(t)
	orders := "api/orders/orders.go"

	first := w.commit(t, "the e2e suite on main")
	w.plan(t, first, true)

	if sp := w.e2ePlan(t, first); len(sp.Selected) != 2 {
		t.Fatalf("the first full round runs %v", selectedNames(sp))
	}

	w.e2eJob(t, first, map[string]string{"e2e.checkout": "passed", "e2e.orders": "failed"})

	// what changed since reaches orders alone: checkout passed a moment ago and keeps that pass;
	// orders failed and runs again
	next := w.change(t, orders, "orders, fixed")
	sp := w.e2ePlan(t, next)

	dropped, reasons := passedSinceOf(sp)
	if selectedNames(sp) != "e2e.orders" || !slices.Equal(dropped, []string{"e2e.checkout"}) {
		t.Fatalf("runs %v, left out %v", selectedNames(sp), dropped)
	}

	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], "compared from "+first[:12]) {
		t.Fatalf("the reason %q", reasons)
	}

	w.e2eJob(t, next, map[string]string{"e2e.orders": "passed"})

	// a change both reach runs both again
	shared := w.change(t, "web/app.ts", "a change both slices reach")
	if sp := w.e2ePlan(t, shared); selectedNames(sp) != "e2e.checkout,e2e.orders" {
		t.Fatalf("after a change both reach: runs %v", selectedNames(sp))
	}
}
