package plugins

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/plugin"
)

// sneaky is a links correlator whose set for a.txt names only a.txt, but
// links a to b only while b.txt says "link": its answer depends on a file
// it doesn't declare.
func sneaky() map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindCorrelator, Name: "uses",
			Capabilities: []string{plugin.CallLinks},
			Inputs:       map[string]string{plugin.CallLinks: plugin.PerSet},
		}),
		plugin.CallLinks: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			b, err := os.ReadFile(filepath.Join(req.Tree, "texts/b.txt"))
			if err != nil {
				return nil, fmt.Errorf("links: %w", err)
			}

			var a plugin.LinksAnswer

			for _, f := range []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"} {
				if len(req.Paths) > 0 && !slices.Contains(req.Paths, f) {
					continue
				}

				set := plugin.Set{Group: f, Inputs: []string{f}}
				if f == "texts/a.txt" && strings.Contains(string(b), "link") {
					set.Links = []plugin.Link{{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkFollow}}
				}

				a.Sets = append(a.Sets, set)
			}

			return a, nil
		},
	}
}

func (s *setup) cold(t *testing.T) *Set {
	t.Helper()

	set, err := Load(t.Context(), Options{Config: s.cfg, Dir: s.dir, Cache: t.TempDir(), Answers: s.cache, Work: t.TempDir(), Cold: true})
	if err != nil {
		t.Fatal(err)
	}

	return set
}

func TestAColdPassFindsAnAnswerThatDependsOnAnUndeclaredFile(t *testing.T) {
	s := newSetup(t, "sneaky")

	if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c1", 3); err != nil {
		t.Fatal(err)
	}

	s.r.Write("texts/b.txt", "link\n")
	s.r.Commit("b links")

	warm, err := s.set(t).Tree(t.Context(), s.r.Dir, "c2", 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(warm.Typed) != 0 {
		t.Fatalf("warm, the cache was expected to serve a's old set: %v", warm.Typed)
	}

	cold := s.cold(t)

	tree, err := cold.Tree(t.Context(), s.r.Dir, "c2", 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Typed) != 1 {
		t.Errorf("cold links %v", tree.Typed)
	}

	d := cold.Differences()
	if len(d) != 1 || d[0].Plugin != "uses" || d[0].Call != plugin.CallLinks || !slices.Equal(d[0].Inputs, []string{"texts/a.txt"}) ||
		len(d[0].What) != 1 || !strings.Contains(d[0].What[0], `link {"from":"t:a.txt","to":"t:b.txt","kind":"follow"}: fresh only`) {
		t.Errorf("differences %+v", d)
	}
}

func TestAColdPassOverAWellBehavedPlugin(t *testing.T) {
	s := newSetup(t, "typed")

	warm, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 2)
	if err != nil {
		t.Fatal(err)
	}

	before := cacheFiles(t, s.cache.Dir)

	cold := s.cold(t)

	tree, err := cold.Tree(t.Context(), s.r.Dir, "c", 2)
	if err != nil {
		t.Fatal(err)
	}

	if d := cold.Differences(); len(d) != 0 {
		t.Errorf("differences %+v", d)
	}

	if a, b := fullLinks(t, tree.Typed, false), fullLinks(t, warm.Typed, false); !slices.Equal(a, b) || !maps.Equal(tree.Units, warm.Units) {
		t.Errorf("cold %v, warm %v", a, b)
	}

	for _, c := range cold.Report().Calls {
		if c.Cached != 0 {
			t.Errorf("a cold pass served %+v from the cache", c)
		}
	}

	if after := cacheFiles(t, s.cache.Dir); !maps.Equal(before, after) {
		t.Errorf("a cold pass over a planned tree wrote %d files, had %d", len(after), len(before))
	}
}

func cacheFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p) //nolint:gosec // the test's own cache
		out[p] = string(data)

		return err //nolint:wrapcheck // the walk ends with it
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestAColdPassComparesWithTheSharedLevel(t *testing.T) {
	for _, c := range []struct {
		what           string
		shared, record bool
		differs        bool
	}{
		{"the shared answer differs", true, false, true},
		{"the local member records differ", true, true, true},
		{"no shared level", false, false, false},
	} {
		t.Run(c.what, func(t *testing.T) {
			r := &onceRemote{answers: map[string][]byte{}, items: map[string][]string{}}
			s, _ := importerSetup(t)
			local := t.TempDir()
			s.cache = &plugincache.Cache{Dir: local, Remote: r, Push: true}

			if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}

			if err := s.cache.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}

			tampered := 0

			for k, data := range r.answers {
				if strings.Contains(string(data), `"inputs_id"`) && strings.Contains(string(data), `"to":"t:a.txt"`) {
					r.answers[k] = []byte(strings.Replace(string(data), `"to":"t:a.txt"`, `"to":"t:doc.txt"`, 1))
					tampered++
				}
			}

			if tampered != 1 {
				t.Fatalf("tampered with %d answers", tampered)
			}

			if c.record {
				emptyMemberRecords(t, local)
			}

			if !c.shared {
				s.cache = &plugincache.Cache{Dir: local}
			}

			cold := s.cold(t)
			if _, err := cold.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}

			d := cold.Differences()

			switch {
			case !c.differs && len(d) > 0:
				t.Errorf("differences %+v", d)
			case c.differs && (len(d) != 1 || !slices.Equal(d[0].Inputs, []string{"texts/b.txt"}) ||
				!slices.ContainsFunc(d[0].What, func(w string) bool {
					return strings.Contains(w, `"to":"t:doc.txt"`) && strings.HasSuffix(w, "cached only")
				})):
				t.Errorf("differences %+v", d)
			}
		})
	}
}

func emptyMemberRecords(t *testing.T, dir string) {
	t.Helper()

	digest := regexp.MustCompile(`^[0-9a-f]{32}$`)
	emptied := 0

	for p, data := range cacheFiles(t, dir) {
		var items []string
		if json.Unmarshal([]byte(data), &items) != nil || len(items) == 0 || !slices.ContainsFunc(items, digest.MatchString) {
			continue
		}

		if err := os.WriteFile(p, []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}

		emptied++
	}

	if emptied == 0 {
		t.Fatal("no member record found")
	}
}

func TestCompleteIsPartOfTheAnswer(t *testing.T) {
	s := newSetup(t, "links")
	s.r.Write("texts/doc.txt", "whole\n")
	s.r.Commit("doc is whole")

	first, err := s.set(t).Tree(t.Context(), s.r.Dir, "c1", 3)
	if err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprint(first.Complete); got != "map[texts/doc.txt:texts]" {
		t.Fatalf("complete %s", got)
	}

	again, err := s.set(t).Tree(t.Context(), s.r.Dir, "c1", 3)
	if err != nil {
		t.Fatal(err)
	}

	if !maps.Equal(again.Complete, first.Complete) {
		t.Fatalf("from the cache %v, asked %v", again.Complete, first.Complete)
	}

	s.r.Write("texts/b.txt", "whole too\n")
	s.r.Commit("b says a is whole")

	warm, err := s.set(t).Tree(t.Context(), s.r.Dir, "c2", 3)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := warm.Complete["texts/a.txt"]; ok {
		t.Fatalf("warm, the cache was expected to serve a's old answer: %v", warm.Complete)
	}

	cold := s.cold(t)
	if _, err := cold.Tree(t.Context(), s.r.Dir, "c2", 3); err != nil {
		t.Fatal(err)
	}

	d := cold.Differences()
	if len(d) != 1 || d[0].Plugin != "texts" || !slices.Equal(d[0].Inputs, []string{"texts/a.txt"}) ||
		!slices.Contains(d[0].What, "complete texts/a.txt: fresh only") {
		t.Errorf("differences %+v", d)
	}
}
