package affected

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestSnapshotsAreKeyedByTheTools(t *testing.T) {
	r := testrepo.New(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	head := r.Git("rev-parse", "HEAD")
	o := Options{Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: t.TempDir(), ToolKey: "build a"}

	key := func(o Options) string {
		t.Helper()

		k, _, err := prepareTree(t.Context(), o, head)
		if err != nil {
			t.Fatal(err)
		}

		return k
	}

	a := key(o)
	if again := key(o); again != a {
		t.Fatalf("the same tools: %s, then %s", a, again)
	}

	o.ToolKey = "build b"
	if b := key(o); b == a {
		t.Fatalf("other tools, the same key %s", a)
	}
}
