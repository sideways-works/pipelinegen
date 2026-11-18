package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// typed answers a set per text; a.txt's holds a link of every typed kind and form (linked-only,
// reference, narrow, to the core's ids, to a unit nobody lists) and one plain link.
func typed() map[string]plugin.Handler {
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

			links := typedLinks()
			if k := os.Getenv("FAKE_KIND"); k != "" {
				links[0].Kind = k
			}

			var a plugin.LinksAnswer

			for _, f := range []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"} {
				if len(req.Paths) > 0 && !slices.Contains(req.Paths, f) {
					continue
				}

				set := plugin.Set{Group: f, Inputs: []string{f}}
				if f == "texts/a.txt" {
					set.Links = links
				}

				if os.Getenv("FAKE_PLAIN_ONLY") != "" {
					set.Links = slices.DeleteFunc(slices.Clone(set.Links), plugin.Link.Typed)
				}

				if os.Getenv("FAKE_ALL_LISTED") != "" {
					set.Links = slices.DeleteFunc(slices.Clone(set.Links), func(l plugin.Link) bool { return l.To == "t:nowhere" })
				}

				a.Sets = append(a.Sets, set)
			}

			return a, nil
		},
	}
}

func typedLinks() []plugin.Link {
	return []plugin.Link{
		{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkFollow},
		{From: "t:a.txt", To: "t:doc.txt", Kind: plugin.LinkBind, IfLinked: true},
		{From: "t:b.txt", To: "t:a.txt", Kind: plugin.LinkDependentsOnly},
		{From: "t:a.txt", To: "t:doc.txt#intro", Kind: plugin.LinkBind, Narrowing: "parts", While: plugin.WhileOn},
		{From: "t:a.txt", To: "t:doc.txt", Kind: plugin.LinkFollow, Narrowing: "parts"},
		{From: "t:a.txt", To: "t:nowhere", Kind: plugin.LinkDependentsOnly, Ref: true},
		{From: "t:a.txt", To: "group:texts", Kind: plugin.LinkFollow},
		{From: "group:texts", To: "node:t:binary", Kind: plugin.LinkFollow},
		{From: "t:b.txt", To: "t:a.txt", Kind: plugin.LinkUses},
	}
}

// withFake points the setup's correlator at another fake; its variables come from a file beside
// it, since a plugin gets none of ours.
func withFake(t *testing.T, s *setup, fake string, env ...string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	vars := filepath.Join(s.dir, ".uses.env")
	if err := os.WriteFile(vars, []byte(strings.Join(append([]string{"FAKE=" + fake}, env...), "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := "#!/bin/sh\nset -a\n. '" + vars + "'\nset +a\nexec '" + exe + "' \"$@\"\n"
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, "uses"), []byte(script)); err != nil {
		t.Fatal(err)
	}
}

func fullLinks(t *testing.T, links []plugin.Link, sorted bool) []string {
	t.Helper()

	out := make([]string, 0, len(links))

	for _, l := range links {
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, string(data))
	}

	if sorted {
		slices.Sort(out)
	}

	return out
}

func TestTypedLinksKeepTheirKindsThroughTheCache(t *testing.T) {
	s := newSetup(t, "typed")

	for _, level := range []int{2, 3} {
		cold, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", level)
		if err != nil {
			t.Fatal(err)
		}

		set := s.set(t)

		warm, err := set.Tree(t.Context(), s.r.Dir, "c", level)
		if err != nil {
			t.Fatal(err)
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "uses" && c.Asked != 0 {
				t.Errorf("level %d, warm: links asked again: %+v", level, c)
			}
		}

		for _, tr := range []*deps.Tree{cold, warm} {
			if got := strings.Join(linkSet(tr.Links), " "); got != "t:b.txt>t:a.txt" {
				t.Errorf("level %d: plain links %s", level, got)
			}

			if len(tr.RuledOut) != 0 {
				t.Errorf("level %d: plain links ruled out %v", level, tr.RuledOut)
			}
		}

		if a, b := fullLinks(t, warm.Typed, false), fullLinks(t, cold.Typed, false); !slices.Equal(a, b) {
			t.Errorf("level %d: warm typed links\n%s\ncold\n%s", level, strings.Join(a, "\n"), strings.Join(b, "\n"))
		}

		if a, b := fullLinks(t, warm.TypedRuledOut["parts"], false), fullLinks(t, cold.TypedRuledOut["parts"], false); !slices.Equal(a, b) {
			t.Errorf("level %d: warm ruled out %v, cold %v", level, a, b)
		}

		got := fullLinks(t, slices.Concat(warm.Typed, warm.TypedRuledOut["parts"]), true)
		want := slices.DeleteFunc(typedLinks(), func(l plugin.Link) bool { return !l.Typed() })

		if level == 3 {
			want = slices.DeleteFunc(want, func(l plugin.Link) bool { return l.While == plugin.WhileOn })
		}

		if w := fullLinks(t, want, true); !slices.Equal(got, w) {
			t.Errorf("level %d: typed links\n%s\nwant\n%s", level, strings.Join(got, "\n"), strings.Join(w, "\n"))
		}

		if u := warm.Units["t:alias"]; u.File != "texts/a.txt" || u.Entry != "TestAlias" || !u.NotRoot || u != cold.Units["t:alias"] {
			t.Errorf("level %d: alias %+v, cold %+v", level, u, cold.Units["t:alias"])
		}

		if why := warm.CannotTell["t:a.txt"]; why != "links to t:nowhere, which no plugin describes" {
			t.Errorf("level %d: a's reason %q", level, why)
		}

		for _, id := range []string{"group:texts", "node:t:binary"} {
			if _, ok := warm.CannotTell[id]; ok {
				t.Errorf("level %d: %s binds wide", level, id)
			}
		}

		if w := strings.Join(set.Report().Warnings, "\n"); !strings.Contains(w, "plugin uses: a dependents-only link from t:a.txt to t:nowhere") {
			t.Errorf("level %d: warnings %q", level, w)
		}
	}
}

func TestTypedLinksChangeNoBinding(t *testing.T) {
	bind := func(env ...string) []deps.Slice {
		t.Helper()

		s := newSetup(t, "typed")
		withFake(t, s, "typed", env...)

		set := s.set(t)

		tree, err := set.Tree(t.Context(), s.r.Dir, "c", 3)
		if err != nil {
			t.Fatal(err)
		}

		out, err := set.Bind(t.Context(), s.r.Dir, tree, []BindSlice{
			{Suite: "s", Name: "a_test", HasList: true, List: []string{"t:a.txt"}},
			{Suite: "s", Name: "doc_test", HasList: true, List: []string{"t:doc.txt"}},
		})
		if err != nil {
			t.Fatal(err)
		}

		return out
	}

	with, without := bind("FAKE_ALL_LISTED=1"), bind("FAKE_ALL_LISTED=1", "FAKE_PLAIN_ONLY=1")

	if len(with) != len(without) || len(with) == 0 {
		t.Fatalf("%d slices with typed links, %d without", len(with), len(without))
	}

	for i := range with {
		a, b := with[i], without[i]
		if a.Name != b.Name || a.Width != b.Width || !slices.Equal(a.Units, b.Units) || !slices.Equal(a.Groups, b.Groups) || a.Everything != b.Everything {
			t.Errorf("%s %s: with typed links %v %v, without %v %v", a.Name, a.Width, a.Units, a.Groups, b.Units, b.Groups)
		}
	}
}

func TestAMalformedLinkFailsThePlan(t *testing.T) {
	for _, kind := range []string{"dependents_only", "Follow", "BIND"} {
		s := newSetup(t, "typed")
		withFake(t, s, "typed", "FAKE_KIND="+kind)

		_, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 2)
		if err == nil || !strings.Contains(err.Error(), "kind \""+kind+"\"") {
			t.Errorf("%s: got %v", kind, err)
		}

		withFake(t, s, "typed")

		tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 2)
		if err != nil {
			t.Fatal(err)
		}

		if slices.ContainsFunc(tree.Links, func(l plugin.Link) bool { return l.Kind == kind }) {
			t.Errorf("%s: served from the cache", kind)
		}
	}
}

func TestAlikeTypedLinksAreOne(t *testing.T) {
	got := mergeLinks([]plugin.Link{
		{From: "a", To: "m", Kind: plugin.LinkBind, IfLinked: true},
		{From: "a", To: "m", Kind: plugin.LinkBind, Ref: true},
		{From: "a", To: "m", Kind: plugin.LinkBind, Narrowing: "n"},
		{From: "a", To: "i", Kind: plugin.LinkDependentsOnly, IfLinked: true},
		{From: "a", To: "i", Kind: plugin.LinkDependentsOnly, IfLinked: true},
		{From: "a", To: "m", Kind: plugin.LinkFollow},
	})

	want := []plugin.Link{
		{From: "a", To: "i", Kind: plugin.LinkDependentsOnly, IfLinked: true},
		{From: "a", To: "m", Kind: plugin.LinkBind, Ref: true},
		{From: "a", To: "m", Kind: plugin.LinkBind, Narrowing: "n"},
		{From: "a", To: "m", Kind: plugin.LinkFollow},
	}

	if !slices.Equal(got, want) {
		t.Errorf("got\n%v\nwant\n%v", got, want)
	}
}
