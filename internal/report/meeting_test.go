package report

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

var errNotRecorded = errors.New("not recorded as passed")

// busyRemote is a bare remote whose pre-receive hook refuses the first
// push of every writer (told apart by the first ref it pushes), as a host
// busy with another push to the same repository does.
func busyRemote(t *testing.T) string {
	t.Helper()

	remote := bareRemote(t)
	state := t.TempDir()
	hook := `#!/bin/sh
read -r old new ref
mark="` + state + `/$(printf %s "$ref" | tr / _)"
[ -e "$mark" ] && exit 0
: > "$mark"
echo "busy with another push" >&2
exit 1
`

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(strings.TrimPrefix(remote, "file://"), "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	return remote
}

func TestBatchEndsThatMeetAreAllRecorded(t *testing.T) {
	const batches = 6

	for _, remote := range []string{bareRemote(t), busyRemote(t)} {
		ctx := t.Context()
		gh := newFake()

		var selected []string
		for i := range batches {
			selected = append(selected, fmt.Sprintf(`{"label": "//e2e:s%d", "sources": []}`, i))
		}

		first := newReporter(t, remote, gh, "")
		first.Store.Retries = 10

		plan := `{"base": "b", "head": "h", "suites": [{"suite": "e2e", "selected": [` + strings.Join(selected, ", ") +
			`], "untouched": [], "whole_suite": []}]}`
		if err := WritePlan(ctx, first.Store, first.ID, []byte(plan)); err != nil {
			t.Fatal(err)
		}

		var (
			wg   sync.WaitGroup
			errs = make([]error, batches)
		)

		for i := range batches {
			r := newReporter(t, remote, gh, "")
			r.Store.Retries = 10
			r.Observer = fakeObserver{rec: []byte("x#func:X\n")}
			ev := SliceEvent{
				Suite: "e2e", Slice: fmt.Sprintf("e2e.s%d", i), Label: fmt.Sprintf("//e2e:s%d", i),
				Result: writeResult(t, `{"passed": true}`), Coverage: []string{"cov"},
			}

			wg.Go(func() {
				states, err := r.Close(ctx, []SliceEvent{ev})
				if err == nil && states["e2e/"+ev.Slice] != Passed {
					err = fmt.Errorf("%w: %s: %v", errNotRecorded, ev.Slice, states)
				}

				errs[i] = err
			})
		}

		wg.Wait()

		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}

		got, err := States(ctx, first.Store, first.ID, "e2e")
		if err != nil || len(got) != batches {
			t.Fatalf("states %v %v", got, err)
		}

		for name, st := range got {
			if st != Passed {
				t.Fatalf("%s: %s", name, st)
			}
		}

		if ok, err := first.Suite(ctx, "e2e"); err != nil || !ok {
			t.Fatalf("the suite: %v %v", ok, err)
		}
	}
}
