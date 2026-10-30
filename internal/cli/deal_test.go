package cli

import (
	"fmt"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestSlicesAreDealtIntoTheSuitesBatches(t *testing.T) {
	todo := make([]bazel.Slice, 0, 10)
	for i := range 10 {
		todo = append(todo, bazel.Slice{Name: fmt.Sprintf("s%d.spec.ts", i)})
	}

	for _, c := range []struct {
		suite config.Suite
		want  int
	}{
		{config.Suite{Parallel: 2}, 2},
		{config.Suite{Parallel: 2, Batches: 5}, 5},
		{config.Suite{}, config.DefaultParallel},
	} {
		dealt := deal(t.TempDir(), c.suite, todo)
		if len(dealt) != c.want {
			t.Errorf("%+v: %d batches, want %d", c.suite, len(dealt), c.want)
		}

		n := 0
		for _, b := range dealt {
			n += len(b)
		}

		if n != len(todo) {
			t.Errorf("%+v: dealt %d of %d slices", c.suite, n, len(todo))
		}
	}
}

func TestBatchMinutesDealsAChangeSideBySide(t *testing.T) {
	todo := func(n int) []bazel.Slice {
		out := make([]bazel.Slice, n)
		for i := range out {
			out[i] = bazel.Slice{Name: fmt.Sprintf("e2e/s%03d.spec.ts", i)}
		}

		return out
	}

	s := config.Suite{BatchMinutes: 18, Parallel: 2}
	if got := deal(t.TempDir(), s, todo(5)); len(got) != 2 {
		t.Fatalf("five slices: %d batches", len(got))
	}

	s.Batches = 3
	if got := deal(t.TempDir(), s, todo(5)); len(got) != 2 {
		t.Fatalf("five slices under batches 3: %d batches", len(got))
	}
}
