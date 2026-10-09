package gitstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func TestWritesOverOthersLandOnAnOldGit(t *testing.T) {
	for _, over := range []string{"file", "http"} {
		t.Run(over, func(t *testing.T) {
			ctx := t.Context()

			remote := bare(t)
			if over == "http" {
				remote = httpRemote(t, false)
			}

			endMark := func(body string) {
				t.Helper()

				if _, err := groupedStore(t, remote, "").Write(ctx, "end", Update{Ref: endRef(), Any: true, Files: files("end.json", body)}); err != nil {
					t.Fatalf("end %s: %v", body, err)
				}
			}

			state(t, groupedStore(t, remote, "1"), "a.spec.ts", "running")
			endMark("first")

			testenv.OldGit(t)

			state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")
			endMark("again")

			if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
				t.Fatal(err)
			}

			got := readAll(t, remote)
			if got[endRef()] != "end.json=again" || !strings.Contains(got[stateRef("a.spec.ts")], "state.json=passed") {
				t.Errorf("records %v", got)
			}
		})
	}
}

func TestABaseTheRemoteCannotGiveIsALostRead(t *testing.T) {
	ctx := t.Context()

	g, err := groupedStore(t, bare(t), "1").partial(ctx)
	if err != nil {
		t.Fatal(err)
	}

	testenv.OldGit(t)

	err = g.fetchMissing(ctx, []string{"0123456789abcdef0123456789abcdef01234567"})
	if !errors.Is(err, errTransport) || errors.Is(err, ErrMissing) {
		t.Fatalf("got %v", err)
	}
}
