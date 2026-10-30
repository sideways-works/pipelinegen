package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestGoneListsSelectAtLeastAsMuch(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("nspecs/BUILD.bazel", globBuild)
	r.Write("nspecs/old.spec.ts", "test('old')\n")
	listed := r.Commit("old spec")

	for range lists.Walk + 1 {
		r.Git("commit", "--quiet", "--allow-empty", "-m", "merge")
	}

	base := strings.TrimSpace(r.Git("rev-parse", "HEAD"))

	r.Write("texts/c.txt", "c changed\n")
	head := r.Commit("change c")

	store := storetest.Open(t, storetest.Bare(t))
	key := "nspecs/nspecs/old.spec.ts"

	if _, err := store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(listed), Files: lists.Encode(recording.Lists{
		key: {"t:c", "t:s"},
	})}); err != nil {
		t.Fatal(err)
	}

	live := report.ID{Run: "pr", Change: "pr-3", Commit: strings.Repeat("ab", 20)}
	recorded(t, store, live, "nspecs", "nspecs/old.spec.ts", report.Passed, "t:s\n")

	got, src, err := planningLists(t.Context(), store, r.Dir, base, lists.Walk)
	if err != nil {
		t.Fatal(err)
	}

	if src == nil || !src.Gone || len(got) != 0 || len(src.Live) != 0 {
		t.Fatalf("lists %v, source %+v; want none, gone, no live run", got, src)
	}

	if text := lists.GoneText(*src); !strings.Contains(text, base[:12]) || !strings.Contains(text, "runs as without a list") {
		t.Errorf("warning %q", text)
	}

	if sel := planSuite(t, r.Dir, "nspecs", base, head, got, differ); sel != "nspecs/old.spec.ts" {
		t.Errorf("with the lists gone the change selects %q, want nspecs/old.spec.ts", sel)
	}

	if sel := planSuite(t, r.Dir, "nspecs", base, head, recording.Lists{key: {"t:s"}}, differ); sel != "" {
		t.Errorf("the narrower recording alone selects %q; the test no longer shows the difference", sel)
	}
}
