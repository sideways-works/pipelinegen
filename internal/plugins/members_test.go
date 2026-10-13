package plugins

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/plugin"
)

func memberRemote(t *testing.T) string {
	t.Helper()

	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	return "file://" + remote
}

func memberPlan(t *testing.T, remote string) *Set {
	t.Helper()

	return &Set{o: Options{Answers: &plugincache.Cache{
		Dir:    t.TempDir(),
		Remote: &plugincache.Refs{Store: &gitstore.Store{Remote: remote, Dir: t.TempDir(), Retries: 3}},
		Push:   true,
	}}}
}

var memberPlugin = &Loaded{Conf: config.Plugin{Name: "uses", Kind: config.KindCorrelator}, P: plugin.Plugin{Name: "uses"}}

func memberSet(inputs ...string) plugin.Set {
	return plugin.Set{Group: inputs[0], Inputs: inputs, Links: []plugin.Link{{From: "go:" + inputs[0], To: "go:x", Kind: plugin.LinkUses}}}
}

func keepAndFlush(t *testing.T, s *Set, files map[string]string, asked []string, sets ...plugin.Set) {
	t.Helper()

	if err := s.keep(t.Context(), memberPlugin, plugin.CallLinks, files, unitDigests{}, sets, asked); err != nil {
		t.Fatal(err)
	}

	if err := s.o.Answers.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func loaded(t *testing.T, remote string, files map[string]string, paths ...string) ([]string, []string) {
	t.Helper()

	claims := make([]plugin.Claim, 0, len(paths))
	for _, p := range paths {
		claims = append(claims, plugin.Claim{Path: p, Role: "go"})
	}

	got, missing, _ := memberPlan(t, remote).cachedSets(t.Context(), memberPlugin, plugin.CallLinks, files, unitDigests{}, claims)

	var sets []string
	for _, set := range got.Sets {
		sets = append(sets, strings.Join(set.Inputs, "+"))
	}

	slices.Sort(sets)

	return sets, missing
}

func TestTwoPlansAskingOneFileAgainKeepBothAnswers(t *testing.T) {
	for _, aFirst := range []bool{true, false} {
		remote := memberRemote(t)
		files := map[string]string{"p.go": "bp", "q.go": "bq", "r.go": "br"}

		keepAndFlush(t, memberPlan(t, remote), files, []string{"p.go"}, memberSet("p.go"))

		a, b := memberPlan(t, remote), memberPlan(t, remote)

		if err := a.keep(t.Context(), memberPlugin, plugin.CallLinks, files, unitDigests{}, []plugin.Set{memberSet("p.go", "q.go")}, []string{"p.go"}); err != nil {
			t.Fatal(err)
		}

		if err := b.keep(t.Context(), memberPlugin, plugin.CallLinks, files, unitDigests{}, []plugin.Set{memberSet("p.go", "r.go")}, []string{"p.go"}); err != nil {
			t.Fatal(err)
		}

		order := []*Set{a, b}
		if !aFirst {
			order = []*Set{b, a}
		}

		for _, s := range order {
			if err := s.o.Answers.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
		}

		sets, missing := loaded(t, remote, files, "p.go")
		if want := []string{"p.go+q.go", "p.go+r.go"}; !slices.Equal(sets, want) || len(missing) != 0 {
			t.Errorf("a first %v: sets %v, asked again %v; want %v and nothing asked", aFirst, sets, missing, want)
		}
	}
}

func TestARevertedFileFindsItsAnswer(t *testing.T) {
	remote := memberRemote(t)
	at := func(blob string) map[string]string { return map[string]string{"p.go": "p" + blob, "q.go": "q" + blob} }

	keepAndFlush(t, memberPlan(t, remote), at("a"), []string{"p.go", "q.go"}, memberSet("p.go", "q.go"))
	keepAndFlush(t, memberPlan(t, remote), at("b"), []string{"p.go", "q.go"}, memberSet("p.go"), memberSet("q.go"))

	sets, missing := loaded(t, remote, at("a"), "p.go", "q.go")
	if want := []string{"p.go+q.go"}; !slices.Equal(sets, want) || len(missing) != 0 {
		t.Errorf("back at a: sets %v, asked again %v; want %v and nothing asked", sets, missing, want)
	}

	sets, missing = loaded(t, remote, at("b"), "p.go", "q.go")
	if want := []string{"p.go", "q.go"}; !slices.Equal(sets, want) || len(missing) != 0 {
		t.Errorf("at b: sets %v, asked again %v; want %v and nothing asked", sets, missing, want)
	}
}
