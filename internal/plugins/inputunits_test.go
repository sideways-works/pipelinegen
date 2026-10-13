package plugins

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestWhatAnInputUnitIsKeyedOn(t *testing.T) {
	u := unitDigests{
		digest:  map[string]string{"go:a#api": "d1", "go:b#api": ""},
		files:   map[string]string{"a/a.go": "blob"},
		covered: map[string]bool{"go": true},
	}

	for _, c := range []struct {
		id, digest string
		ok         bool
	}{
		{"go:a#api", "d1", true},
		{"go:gone#api", "absent", true},
		{"go:b#api", "", false},
		{"ts:x", "", false},
		{"file:a/a.go", "blob", true},
		{"file:gone.go", "absent", true},
	} {
		if d, ok := u.of(c.id); d != c.digest || ok != c.ok {
			t.Errorf("%s: %q %v, want %q %v", c.id, d, ok, c.digest, c.ok)
		}
	}
}

// importer is a links correlator with a set per text; b.txt's set names
// FAKE_UNIT (default a.txt's api unit) as an input unit instead of a.txt,
// as a Go package names what its imports declare, and the sets of b.txt
// and doc.txt name the units in FAKE_UNITS too. With FAKE_SHARED every
// set holds that file too; with FAKE_MARK every set links to doc.txt, an
// answer other than the one before. Each call's paths go to FAKE_LOG.
func importer() map[string]plugin.Handler {
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

			unit := os.Getenv("FAKE_UNIT")
			if unit == "" {
				unit = "t:a.txt#api"
			}

			shared := os.Getenv("FAKE_SHARED")
			all := len(req.Paths) == 0 || shared != "" && slices.Contains(req.Paths, shared)

			var a plugin.LinksAnswer

			for _, f := range []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"} {
				if !all && !slices.Contains(req.Paths, f) {
					continue
				}

				set := plugin.Set{Group: f, Inputs: []string{f}}
				if shared != "" {
					set.Inputs = []string{shared, f}
				}

				if f != "texts/a.txt" {
					set.InputUnits = strings.Fields(os.Getenv("FAKE_UNITS"))
				}

				if f == "texts/b.txt" {
					set.InputUnits = append(set.InputUnits, unit)
					set.Links = []plugin.Link{{From: "t:b.txt", To: "t:a.txt", Kind: plugin.LinkFollow}}
				}

				if os.Getenv("FAKE_MARK") != "" {
					set.Links = append(set.Links, plugin.Link{From: "t:" + strings.TrimPrefix(f, "texts/"), To: "t:doc.txt", Kind: "marks"})
				}

				a.Sets = append(a.Sets, set)
			}

			return a, nil
		},
	}
}

func importerSetup(t *testing.T, env ...string) (*setup, func() []string) {
	t.Helper()

	s := newSetup(t, "importer")
	s.r.Write("texts/a.txt", "api 1\nbody 1\n")
	s.r.Commit("api")

	log := t.TempDir() + "/calls"
	withFake(t, s, "importer", append(env, "FAKE_LOG='"+log+"'")...)

	return s, func() []string {
		data, _ := os.ReadFile(log) //nolint:errcheck // no log is no call
		defer os.Remove(log)        //nolint:errcheck // each step counts its own

		if len(data) == 0 {
			return nil
		}

		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func TestASetKeyedOnAnInputUnit(t *testing.T) {
	s, calls := importerSetup(t)

	plan := func() []string {
		t.Helper()

		tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3)
		if err != nil {
			t.Fatal(err)
		}

		return fullLinks(t, tree.Typed, true)
	}

	cold := plan()
	if len(cold) != 1 {
		t.Fatalf("cold links %v", cold)
	}

	calls()

	if warm := plan(); !slices.Equal(warm, cold) {
		t.Fatalf("warm %v, cold %v", warm, cold)
	}

	if c := calls(); len(c) != 0 {
		t.Fatalf("warm, asked %q", c)
	}

	s.r.Write("texts/a.txt", "api 1\nbody 2\n")
	s.r.Commit("body")

	if got := plan(); !slices.Equal(got, cold) {
		t.Fatalf("after the body changed %v", got)
	}

	if c := calls(); !slices.Equal(c, []string{"texts/a.txt"}) {
		t.Fatalf("after the body changed, asked %q", c)
	}

	s.r.Write("texts/a.txt", "api 2\nbody 2\n")
	s.r.Commit("api")

	if got := plan(); !slices.Equal(got, cold) {
		t.Fatalf("after the api changed %v", got)
	}

	if c := strings.Join(calls(), " "); c != "texts/a.txt texts/b.txt" {
		t.Fatalf("after the api changed, asked %q", c)
	}
}

func TestAnInputUnitNobodyCanTellOfIsNeverCached(t *testing.T) {
	for unit, asked := range map[string]bool{"t:gone#api": false, "x:elsewhere": true} {
		s, calls := importerSetup(t, "FAKE_UNIT="+unit)

		for range 2 {
			if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}
		}

		want := 1
		if asked {
			want = 2
		}

		if c := calls(); len(c) != want || asked && c[1] != "texts/b.txt" {
			t.Errorf("%s: calls %q", unit, c)
		}
	}
}

func TestAnApiChangeAsksOnlyTheStaleSets(t *testing.T) {
	s, calls := importerSetup(t, "FAKE_SHARED=go.mod")
	s.r.Write("go.mod", "module m\n")
	s.r.Commit("module")

	var set *Set

	plan := func() []string {
		t.Helper()

		set = s.set(t)

		tree, err := set.Tree(t.Context(), s.r.Dir, "c", 3)
		if err != nil {
			t.Fatal(err)
		}

		return fullLinks(t, tree.Typed, true)
	}

	cold := plan()

	calls()

	s.r.Write("texts/a.txt", "api 2\nbody 1\n")
	s.r.Commit("api")

	if got := plan(); !slices.Equal(got, cold) {
		t.Fatalf("after the api changed %v, cold %v", got, cold)
	}

	if c := strings.Join(calls(), " "); c != "texts/a.txt texts/b.txt" {
		t.Fatalf("after the api changed, asked %q", c)
	}

	for _, c := range set.Report().Calls {
		if c.Plugin == "uses" && c.Call == plugin.CallLinks && c.Asked != 2 {
			t.Errorf("asked about %d files, want 2", c.Asked)
		}
	}

	if got := plan(); !slices.Equal(got, cold) {
		t.Fatalf("warm again %v", got)
	}

	if c := calls(); len(c) != 0 {
		t.Fatalf("warm again, asked %q", c)
	}
}

func TestAColdPassComparesEverySetTheCacheHolds(t *testing.T) {
	for _, c := range []struct {
		what string
		api  bool
		want []string
	}{
		{"unchanged", false, []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"}},
		{"after an api change", true, []string{"texts/doc.txt"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			s, _ := importerSetup(t, "FAKE_SHARED=go.mod")
			s.r.Write("go.mod", "module m\n")
			s.r.Commit("module")

			if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}

			if c.api {
				s.r.Write("texts/a.txt", "api 2\nbody 1\n")
				s.r.Commit("api")
			}

			withFake(t, s, "importer", "FAKE_SHARED=go.mod", "FAKE_MARK=1", "FAKE_LOG='"+t.TempDir()+"/calls'")

			cold := s.cold(t)
			if _, err := cold.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}

			var got []string

			for _, d := range cold.Differences() {
				if d.Plugin != "uses" || len(d.Inputs) != 2 || d.Inputs[0] != "go.mod" {
					t.Fatalf("difference %+v", d)
				}

				got = append(got, d.Inputs[1])
			}

			slices.Sort(got)

			if !slices.Equal(got, c.want) {
				t.Errorf("compared %v, want %v", got, c.want)
			}
		})
	}
}

func TestASetReplacedUnderAnotherShapeIsNotAskedAgain(t *testing.T) {
	s, calls := importerSetup(t, "FAKE_SHARED=go.mod")
	s.r.Write("go.mod", "module m\n")
	s.r.Commit("module")

	plan := func() {
		t.Helper()

		if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
			t.Fatal(err)
		}
	}

	plan()

	log := t.TempDir() + "/calls"
	withFake(t, s, "importer", "FAKE_SHARED=go.mod", "FAKE_UNIT=t:doc.txt", "FAKE_LOG='"+log+"'")
	s.r.Write("texts/b.txt", "b, now with doc\n")
	s.r.Commit("b names doc")

	plan()

	if data, _ := os.ReadFile(log); strings.TrimSpace(string(data)) != "texts/b.txt" { //nolint:errcheck // no log is no call
		t.Fatalf("after b changed, asked %q", data)
	}

	for i := range 2 {
		_ = os.Remove(log) //nolint:errcheck // each plan counts its own

		plan()

		if data, _ := os.ReadFile(log); len(data) > 0 { //nolint:errcheck // no log is no call
			t.Fatalf("plan %d after the new shape, asked %q", i+1, data)
		}
	}

	calls()
}

func TestTheTreeKeepsItsSetsInputUnits(t *testing.T) {
	s, _ := importerSetup(t, "FAKE_UNITS='t:z#api t:m#api'")

	tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"t:a.txt#api", "t:m#api", "t:z#api"}; !slices.Equal(tree.InputUnits, want) {
		t.Fatalf("input units %v, want %v", tree.InputUnits, want)
	}
}
