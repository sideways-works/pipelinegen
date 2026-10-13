package plugins

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// overlapping is a links correlator whose sets share an input, as per-file
// sets do that all read one contract: a.txt's set and b.txt's each name
// the shared file (FAKE_SHARED) and link to it, and the shared file's own
// set links it to b. Each call's paths go to FAKE_LOG.
func overlapping() map[string]plugin.Handler {
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

			if err := appendLine(os.Getenv("FAKE_LOG"), strings.Join(req.Paths, " ")); err != nil {
				return nil, err
			}

			shared := os.Getenv("FAKE_SHARED")
			unit := func(p string) string { return "t:" + strings.TrimPrefix(p, "texts/") }
			sets := []plugin.Set{
				{Group: "texts/a.txt", Inputs: []string{"texts/a.txt", shared}, Links: []plugin.Link{{From: "t:a.txt", To: unit(shared), Kind: plugin.LinkUses}}},
				{Group: "texts/b.txt", Inputs: []string{"texts/b.txt", shared}, Links: []plugin.Link{{From: "t:b.txt", To: unit(shared), Kind: plugin.LinkUses}}},
				{Group: shared, Inputs: []string{shared}, Links: []plugin.Link{{From: unit(shared), To: "t:b.txt", Kind: plugin.LinkUses}}},
			}

			// a module that replaces another with a local path reads that
			// one's files too: one set's inputs hold the other's whole
			if os.Getenv("FAKE_SHAPE") == "superset" {
				sets = []plugin.Set{
					{Group: "texts/a.txt", Inputs: []string{"texts/a.txt", shared}, Links: []plugin.Link{{From: "t:a.txt", To: unit(shared), Kind: plugin.LinkUses}}},
					{Group: shared, Inputs: []string{shared}, Links: []plugin.Link{{From: unit(shared), To: "t:b.txt", Kind: plugin.LinkUses}}},
					{Group: "texts/b.txt", Inputs: []string{"texts/b.txt"}, Links: []plugin.Link{{From: "t:b.txt", To: "t:a.txt", Kind: plugin.LinkUses}}},
				}
			}

			var a plugin.LinksAnswer

			for _, set := range sets {
				holds := slices.ContainsFunc(set.Inputs, func(in string) bool { return slices.Contains(req.Paths, in) })
				if os.Getenv("FAKE_PARTIAL") != "" {
					holds = slices.Contains(req.Paths, set.Group)
				}

				if len(req.Paths) == 0 || holds {
					a.Sets = append(a.Sets, set)
				}
			}

			return a, nil
		},
	}
}

func TestSetsThatShareAnInputAreAllServedFromTheCache(t *testing.T) {
	for _, c := range []struct{ shared, shape string }{
		{"texts/doc.txt", "shared"}, {"texts/0.txt", "shared"}, {"texts/doc.txt", "superset"}, {"texts/0.txt", "superset"},
	} {
		shared := c.shared

		t.Run(c.shape+" "+shared, func(t *testing.T) {
			s := newSetup(t, "overlap")
			s.r.Write(shared, "shared\n")
			s.r.Commit("shared")

			log := t.TempDir() + "/calls"

			// a plugin gets no variable of ours: its script carries them
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			script := "#!/bin/sh\nFAKE=overlap FAKE_LOG='" + log + "' FAKE_SHARED='" + shared + "' FAKE_SHAPE='" + c.shape + "' exec '" + exe + "' \"$@\"\n"
			if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, "uses"), []byte(script)); err != nil {
				t.Fatal(err)
			}

			links := func() []string {
				t.Helper()

				tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3)
				if err != nil {
					t.Fatal(err)
				}

				got := linkSet(tree.Links)
				slices.Sort(got)

				return got
			}

			calls := func() []string {
				data, _ := os.ReadFile(log) //nolint:errcheck // no log is no call
				defer os.Remove(log)        //nolint:errcheck // each step counts its own

				return strings.Split(strings.TrimSpace(string(data)), "\n")
			}

			cold := links()

			calls()

			if len(cold) != 3 {
				t.Fatalf("cold links %v", cold)
			}

			if warm := links(); !slices.Equal(warm, cold) {
				t.Fatalf("warm links %v, cold %v", warm, cold)
			}

			if c := calls(); len(c) != 1 || c[0] != "" {
				t.Fatalf("warm, the plugin was asked %q", c)
			}

			s.r.Write("texts/b.txt", "b changed\n")
			s.r.Commit("b")

			if changed := links(); !slices.Equal(changed, cold) {
				t.Fatalf("after b changed: links %v, want %v", changed, cold)
			}

			if c := calls(); len(c) != 1 || c[0] != "texts/b.txt" {
				t.Fatalf("after b changed, the plugin was asked %q", c)
			}
		})
	}
}

func TestAPluginThatAnswersFewerSetsIsAskedAgain(t *testing.T) {
	for _, shared := range []string{"texts/doc.txt", "texts/0.txt"} {
		t.Run(shared, func(t *testing.T) {
			s := newSetup(t, "overlap")
			s.r.Write(shared, "shared\n")
			s.r.Commit("shared")

			log := t.TempDir() + "/calls"

			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			script := "#!/bin/sh\nFAKE=overlap FAKE_PARTIAL=1 FAKE_LOG='" + log + "' FAKE_SHARED='" + shared + "' exec '" + exe + "' \"$@\"\n"
			if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, "uses"), []byte(script)); err != nil {
				t.Fatal(err)
			}

			links := func() []string {
				t.Helper()

				tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3)
				if err != nil {
					t.Fatal(err)
				}

				got := linkSet(tree.Links)
				slices.Sort(got)

				return got
			}

			cold := links()
			if len(cold) != 3 {
				t.Fatalf("cold links %v", cold)
			}

			s.r.Write(shared, "shared changed\n")
			s.r.Commit("shared changed")

			if got := links(); !slices.Equal(got, cold) {
				t.Fatalf("after the shared file changed: links %v, want %v", got, cold)
			}
		})
	}
}
