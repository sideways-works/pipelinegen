package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// TestMain doubles as the fake plugins: with FAKE set, the test binary
// serves the call it was started with.
func TestMain(m *testing.M) {
	if kind := os.Getenv("FAKE"); kind != "" {
		testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, fakes(kind)))
	}

	os.Exit(m.Run())
}

func decode(in []byte, v any) error {
	if err := json.Unmarshal(in, v); err != nil {
		return fmt.Errorf("request: %w", err)
	}

	return nil
}

func describe(d plugin.Description) plugin.Handler {
	d.Protocol, d.Version = plugin.Protocol, "v0"

	return func([]byte) (any, error) { return d, nil }
}

var errNoSetupFirst = errors.New("observe: no setup first")

var errSymlinkedTree = errors.New("the tree is reached through a symlink")

func cantTellIn(tree, call string) error {
	if _, err := os.Stat(filepath.Join(tree, "cant-tell", call)); err == nil {
		return fmt.Errorf("%w: cant-tell/%s", plugin.ErrCantTell, call)
	}

	return nil
}

func fakes(kind string) map[string]plugin.Handler {
	switch kind {
	case "packages":
		return packages()
	case "sbom":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindSBOM, Name: "texts", Namespaces: []string{"t"},
				Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
				Candidates:   []string{"texts/*.txt"},
				Inputs:       map[string]string{plugin.CallUnits: plugin.PerFile, plugin.CallDetect: plugin.PerFile},
				Narrowings:   []plugin.Narrowing{{Name: "parts", OffAt: 3, Note: "a text binds the parts read"}},
			}),
			plugin.CallDetect: func(in []byte) (any, error) {
				var req plugin.DetectRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if err := cantTellIn(req.Tree, plugin.CallDetect); err != nil {
					return nil, err
				}

				var a plugin.DetectAnswer

				for _, p := range req.Paths {
					c := plugin.Claim{Path: p, Role: "source"}
					if strings.HasSuffix(p, "doc.txt") {
						c.ByUnits = "parts"
					}

					a.Claims = append(a.Claims, c)
				}

				return a, nil
			},
			plugin.CallUnits: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if err := cantTellIn(req.Tree, plugin.CallUnits); err != nil {
					return nil, err
				}

				// a plugin that walks the tree or maps coverage back to it
				// sees nothing through a symlinked root
				if resolved, err := filepath.EvalSymlinks(req.Tree); err != nil || resolved != req.Tree {
					return nil, fmt.Errorf("units: %w: %s", errSymlinkedTree, req.Tree)
				}

				var a plugin.UnitsAnswer

				for _, p := range req.Paths {
					data, err := os.ReadFile(filepath.Join(req.Tree, p))
					if err != nil {
						return nil, fmt.Errorf("units: %w", err)
					}

					sum := sha256.Sum256(data)
					id := "t:" + strings.TrimPrefix(p, "texts/")
					a.Units = append(a.Units, plugin.Unit{ID: id, File: p, Digest: hex.EncodeToString(sum[:4])})

					// a text that starts "api " has an api unit, whose digest
					// is its first line alone, as a package's declarations
					// without their bodies
					if first, _, _ := strings.Cut(string(data), "\n"); strings.HasPrefix(first, "api ") {
						api := sha256.Sum256([]byte(first))
						a.Units = append(a.Units, plugin.Unit{ID: id + "#api", File: p, Digest: hex.EncodeToString(api[:4])})
					}

					// a text saying "whole" is covered whole by its units;
					// a.txt also when b.txt says "whole too", a file its
					// answer doesn't name
					other, _ := os.ReadFile(filepath.Join(req.Tree, "texts/b.txt")) //nolint:errcheck // a missing file reads empty
					if strings.Contains(string(data), "whole") || p == "texts/a.txt" && strings.Contains(string(other), "whole too") {
						a.Complete = append(a.Complete, p)
					}

					switch id {
					case "t:a.txt":
						a.Units = append(a.Units, plugin.Unit{ID: "t:alias", File: p, DigestFrom: []string{"t:a.txt"}, Entry: "TestAlias", NotRoot: true})
					case "t:b.txt":
						a.Units = append(a.Units, plugin.Unit{ID: "t:bad", File: p, DigestFrom: []string{"t:missing"}})
					}
				}

				return a, nil
			},
		}
	case "observer", "endonly":
		d := plugin.Description{Kind: plugin.KindCorrelator, Name: "uses", Capabilities: []string{plugin.CallObserve, plugin.CallCollect}}
		if kind == "endonly" {
			d.Collects = []string{plugin.CollectSliceEnd}
		}

		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(d),
			plugin.CallCollect: func(in []byte) (any, error) {
				var req plugin.CollectRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if err := os.MkdirAll(req.Dir, 0o750); err != nil {
					return nil, fmt.Errorf("collect: %w", err)
				}

				var claimed strings.Builder
				for _, c := range req.Claimed {
					claimed.WriteString(c.Path + "\n")
				}

				if err := os.WriteFile(filepath.Join(req.Dir, "claimed"), []byte(claimed.String()), 0o600); err != nil {
					return nil, fmt.Errorf("collect: %w", err)
				}

				return plugin.Answer{}, os.WriteFile(filepath.Join(req.Dir, "touched"), []byte("t:a.txt\n"), 0o600)
			},
			plugin.CallObserve: func(in []byte) (any, error) {
				var req plugin.ObserveRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if len(req.Dirs) < 2 || !filepath.IsAbs(req.Dirs[0]) {
					return nil, fmt.Errorf("%w: %q", errNoSetupFirst, req.Dirs)
				}

				var a plugin.ObserveAnswer

				for _, d := range req.Dirs[1:] {
					data, err := os.ReadFile(filepath.Join(d, "touched"))
					if err != nil {
						return nil, plugin.ErrCantTell
					}

					a.Touched = append(a.Touched, strings.Fields(string(data))...)
				}

				for _, c := range req.Claimed {
					a.Touched = append(a.Touched, "file:"+c.Path)
				}

				return a, nil
			},
		}
	case "rules":
		return rulesBinder()
	case "depbinder":
		return depBinder()
	case "binder":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses", Capabilities: []string{plugin.CallBind},
			}),
			plugin.CallBind: func(in []byte) (any, error) {
				var req plugin.Request
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				files := map[string]string{
					"units.jsonl": "\"g:other\"\n",
					"bind.jsonl":  `{"suite":"api","slice":"h_test.go","width":"executed","units":[[0,"e",-1]]}` + "\n",
				}

				var a plugin.Answer

				for _, name := range sortedKeys(files) {
					if err := os.WriteFile(filepath.Join(req.Work, name), []byte(files[name]), 0o600); err != nil {
						return nil, fmt.Errorf("bind: %w", err)
					}

					a.Files = append(a.Files, name)
				}

				return a, nil
			},
		}
	case "dropper":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses", Capabilities: []string{plugin.CallBind},
			}),
			plugin.CallBind: func(in []byte) (any, error) {
				var req plugin.Request
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				files := map[string]string{
					"units.jsonl": "\"g:other\"\n\"g:handler\"\n\"g:store\"\n",
					"bind.jsonl": `{"suite":"api","slice":"h_test.go","width":"executed","units":[[0,"references",1,[1]]],` +
						`"dropped":{"n1":[2]},"dropped_groups":{"n1":["grp"]}}` + "\n",
				}

				var a plugin.Answer

				for _, name := range sortedKeys(files) {
					if err := os.WriteFile(filepath.Join(req.Work, name), []byte(files[name]), 0o600); err != nil {
						return nil, fmt.Errorf("bind: %w", err)
					}

					a.Files = append(a.Files, name)
				}

				return a, nil
			},
		}
	case "setupbinder":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses", Capabilities: []string{plugin.CallBind},
			}),
			plugin.CallBind: func(in []byte) (any, error) {
				var req struct {
					Work       string              `json:"work"`
					Narrowings []string            `json:"narrowings"`
					Setup      map[string][]string `json:"setup"`
					Slices     []struct {
						Name string `json:"name"`
					} `json:"slices"`
				}
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				for _, sl := range req.Slices {
					if strings.HasPrefix(sl.Name, "_setup") {
						return nil, fmt.Errorf("%w: the setup came as slice %s", plugin.ErrCantTell, sl.Name)
					}
				}

				bind := ""
				if slices.Equal(req.Narrowings, []string{"n1"}) && slices.Equal(req.Setup["api"], []string{"g:store"}) {
					bind = `{"suite":"api","slice":"_setup","width":"executed","units":[[0,"init",-1]]}` + "\n"
				}

				var a plugin.Answer

				for name, content := range map[string]string{"units.jsonl": "\"g:other\"\n", "bind.jsonl": bind} {
					if err := os.WriteFile(filepath.Join(req.Work, name), []byte(content), 0o600); err != nil {
						return nil, fmt.Errorf("bind: %w", err)
					}

					a.Files = append(a.Files, name)
				}

				return a, nil
			},
		}
	case "typed":
		return typed()
	case "importer":
		return importer()
	case "sneaky":
		return sneaky()
	case "overlap":
		return overlapping()
	case "chain", "chainout", "chainabsent":
		// a correlator that claims texts/a.txt alone and reads along a
		// chain of references, a.txt names the file it reads, which names
		// a third: the set declares all three as its inputs, and its link
		// goes to what the third file says. chainout also declares a path
		// above the tree; chainabsent a config it probed and didn't find.
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses",
				Capabilities: []string{plugin.CallDetect, plugin.CallLinks},
				Candidates:   []string{"texts/a.txt"},
				Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallLinks: plugin.PerSet},
			}),
			plugin.CallDetect: func(in []byte) (any, error) {
				var req plugin.DetectRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				var a plugin.DetectAnswer
				for _, p := range req.Paths {
					a.Claims = append(a.Claims, plugin.Claim{Path: p, Role: "source"})
				}

				return a, nil
			},
			plugin.CallLinks: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				read := func(p string) string {
					data, _ := os.ReadFile(filepath.Join(req.Tree, p)) //nolint:errcheck // a missing file reads empty
					first, _, _ := strings.Cut(string(data), "\n")

					return strings.TrimSpace(first)
				}

				var a plugin.LinksAnswer

				for _, c := range req.Claimed {
					second := read(c.Path)
					third := read(second)

					set := plugin.Set{Group: c.Path, Inputs: []string{c.Path, second, third}}
					set.Links = []plugin.Link{{From: "t:a.txt", To: "t:" + read(third), Kind: plugin.LinkUses}}

					if kind == "chainout" {
						set.Inputs = append(set.Inputs, "../tsconfig.json")
					}

					if kind == "chainabsent" {
						set.Absent = []string{"texts/tsconfig.json"}
						if cfg := read("texts/tsconfig.json"); cfg != "" {
							set.Links = append(set.Links, plugin.Link{From: "t:a.txt", To: "t:" + cfg, Kind: plugin.LinkUses})
						}
					}

					a.Sets = append(a.Sets, set)
				}

				return a, nil
			},
		}
	case "setdigest":
		// a correlator that says, in a.txt's set, that a's content derives
		// from b's
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

				var a plugin.LinksAnswer

				for _, c := range req.Claimed {
					if len(req.Paths) > 0 && !slices.Contains(req.Paths, c.Path) {
						continue
					}

					set := plugin.Set{Group: c.Path, Inputs: []string{c.Path}}
					if c.Path == "texts/doc.txt" {
						set.DigestFrom = []plugin.DigestFrom{{Unit: "t:doc.txt", From: []string{"t:b.txt"}}}
					}

					a.Sets = append(a.Sets, set)
				}

				return a, nil
			},
		}
	case "rest":
		// links in sets, and, once the tree holds rest-on, one outside them
		// too; every call's paths go to FAKE_LOG, one line per call
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses",
				Capabilities: []string{plugin.CallLinks},
				Inputs:       map[string]string{plugin.CallLinks: plugin.PerFile},
			}),
			plugin.CallLinks: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if err := appendLine(os.Getenv("FAKE_LOG"), strings.Join(req.Paths, " ")); err != nil {
					return nil, err
				}

				var a plugin.LinksAnswer

				for _, p := range req.Paths {
					a.Sets = append(a.Sets, plugin.Set{Group: p, Inputs: []string{p}})
				}

				if _, err := os.Stat(filepath.Join(req.Tree, "rest-on")); err == nil {
					a.Links = []plugin.Link{{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkUses}}
				}

				return a, nil
			},
		}
	case "typechecks":
		// a correlator that can't type-check what a.txt says, as a
		// language's correlator can't for a package that doesn't build
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{
				Kind: plugin.KindCorrelator, Name: "uses",
				Capabilities: []string{plugin.CallLinks},
				Inputs:       map[string]string{plugin.CallLinks: plugin.PerSet},
			}),
			plugin.CallLinks: func([]byte) (any, error) {
				return plugin.LinksAnswer{Sets: []plugin.Set{{
					Group: "texts/a.txt", Inputs: []string{"texts/a.txt"},
					CannotTell: []plugin.CannotTell{{Unit: "t:a.txt", Reason: "a.txt does not type-check"}},
				}}}, nil
			},
		}
	case "links", "undeclared":
		narrowing := "parts"
		if kind == "undeclared" {
			narrowing = "nobody-declares-this"
		}

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

				var a plugin.LinksAnswer

				for _, c := range req.Claimed {
					if c.Path != "texts/a.txt" && c.Path != "texts/b.txt" {
						continue
					}

					if len(req.Paths) > 0 && !slices.Contains(req.Paths, c.Path) {
						continue
					}

					set := plugin.Set{Group: c.Path, Inputs: []string{c.Path}}

					if c.Path == "texts/a.txt" {
						set.Links = []plugin.Link{
							{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkUses},
							{From: "t:a.txt", To: "t:doc.txt", Kind: plugin.LinkReads, Narrowing: narrowing},
							{From: "t:a.txt", To: "t:doc.txt#intro", Kind: plugin.LinkReads, Narrowing: narrowing, While: plugin.WhileOn},
						}
					} else {
						set.Links = []plugin.Link{{From: "t:b.txt", To: "t:nowhere", Kind: plugin.LinkUses}}
					}

					a.Sets = append(a.Sets, set)
				}

				return a, nil
			},
		}
	}

	return nil
}

type setup struct {
	r     *testrepo.Repo
	cfg   *config.Config
	dir   string
	cache *plugincache.Cache
}

func newSetup(t *testing.T, correlator string) *setup {
	t.Helper()

	r := testrepo.New(t)
	for _, f := range []string{"a", "b", "doc"} {
		r.Write("texts/"+f+".txt", f+"\n")
	}

	r.Commit("texts")

	s := &setup{r: r, dir: t.TempDir(), cache: &plugincache.Cache{Dir: t.TempDir()}}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for name, kind := range map[string]string{"texts": "sbom", "uses": correlator} {
		script := "#!/bin/sh\nFAKE=" + kind + " exec '" + exe + "' \"$@\"\n"
		if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, name), []byte(script)); err != nil {
			t.Fatal(err)
		}
	}

	rel := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}
	s.cfg = &config.Config{Plugins: []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: rel},
		{Name: "uses", Kind: config.KindCorrelator, Repository: "o/uses", Release: rel},
	}}

	return s
}

func (s *setup) set(t *testing.T) *Set {
	t.Helper()

	set, err := Load(t.Context(), Options{Config: s.cfg, Dir: s.dir, Cache: t.TempDir(), Answers: s.cache, Work: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	return set
}

func linkSet(links []plugin.Link) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.From+">"+l.To)
	}

	return out
}

func TestTreeAtTwoLevels(t *testing.T) {
	s := newSetup(t, "links")
	set := s.set(t)

	at2, err := set.Tree(t.Context(), s.r.Dir, "c", 2)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(linkSet(at2.Links), " "); got != "t:a.txt>t:b.txt t:a.txt>t:doc.txt#intro t:b.txt>t:nowhere" {
		t.Errorf("links at 2 (parts on): %s", got)
	}

	if at2.Units["t:alias"].Digest == "" || at2.Units["t:alias"].Digest == at2.Units["t:a.txt"].Digest {
		t.Errorf("alias digest %+v", at2.Units["t:alias"])
	}

	for _, id := range []string{"t:bad", "t:b.txt", "t:a.txt"} {
		if _, ok := at2.CannotTell[id]; !ok {
			t.Errorf("%s binds wide: %v", id, at2.CannotTell)
		}
	}

	if by := at2.CannotTellBy["t:b.txt"]; by != "uses" {
		t.Errorf("t:b.txt's link to nothing is said by %q, want uses", by)
	}

	if got := at2.FileUnits["texts/doc.txt"]; !slices.Equal(got, []string{"t:doc.txt"}) {
		t.Errorf("file units at 2: %v", at2.FileUnits)
	}

	at3, err := set.Tree(t.Context(), s.r.Dir, "c", 3)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(linkSet(at3.Links), " "); got != "t:a.txt>t:b.txt t:a.txt>t:doc.txt t:b.txt>t:nowhere" {
		t.Errorf("links at 3 (parts off): %s", got)
	}

	if len(at3.FileUnits) != 0 {
		t.Errorf("file units at 3: %v", at3.FileUnits)
	}
}

func TestASymlinkedRootReachesPluginsResolved(t *testing.T) {
	s := newSetup(t, "links")

	link := filepath.Join(t.TempDir(), "tree")
	if err := os.Symlink(s.r.Dir, link); err != nil {
		t.Fatal(err)
	}

	through, err := s.set(t).Tree(t.Context(), link, "c", 3)
	if err != nil {
		t.Fatal(err)
	}

	direct, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(through.Units) == 0 || !maps.Equal(direct.Units, through.Units) {
		t.Fatalf("units through the symlink %v, by the real path %v", through.Units, direct.Units)
	}
}

func TestASetsDigestFromIsCached(t *testing.T) {
	s := newSetup(t, "setdigest")

	first, err := s.set(t).Tree(t.Context(), s.r.Dir, "c1", 3)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("t:b.txt=" + first.Units["t:b.txt"].Digest + "\n"))
	if want := hex.EncodeToString(sum[:]); first.Units["t:doc.txt"].Digest != want {
		t.Fatalf("doc's digest %q, want %q (from b's)", first.Units["t:doc.txt"].Digest, want)
	}

	set := s.set(t)

	again, err := set.Tree(t.Context(), s.r.Dir, "c1", 3)
	if err != nil {
		t.Fatal(err)
	}

	if again.Units["t:doc.txt"].Digest != first.Units["t:doc.txt"].Digest {
		t.Errorf("from the cache, doc's digest %q, want %q", again.Units["t:doc.txt"].Digest, first.Units["t:doc.txt"].Digest)
	}

	for _, c := range set.Report().Calls {
		if c.Plugin == "uses" && c.Call == plugin.CallLinks && c.Asked != 0 {
			t.Errorf("links asked again: %+v", c)
		}
	}
}

func TestAPlanAsksOnlyAboutChangedFiles(t *testing.T) {
	s := newSetup(t, "links")

	if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c1", 2); err != nil {
		t.Fatal(err)
	}

	s.r.Write("texts/a.txt", "a, changed\n")
	s.r.Commit("a changed")

	set := s.set(t)

	tree, err := set.Tree(t.Context(), s.r.Dir, "c2", 2)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree.Units) != 7 || tree.Units[PinID("texts")].ID == "" || tree.Units[PinID("uses")].ID == "" {
		t.Errorf("units %v", tree.Units)
	}

	want := map[string]CallCount{
		"texts/detect": {Plugin: "texts", Call: "detect", Asked: 1, Cached: 2},
		"texts/units":  {Plugin: "texts", Call: "units", Asked: 1, Cached: 2},
	}

	for _, c := range set.Report().Calls {
		if w, ok := want[c.Plugin+"/"+c.Call]; ok && w != c {
			t.Errorf("%+v, want %+v", c, w)
		}

		if c.Plugin == "uses" && c.Call == plugin.CallLinks && (c.Asked != 1 || c.Cached < 2) {
			t.Errorf("links: %+v", c)
		}
	}
}

func TestANarrowingNobodyDeclaresFailsThePlan(t *testing.T) {
	s := newSetup(t, "undeclared")

	if _, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 2); err == nil || !strings.Contains(err.Error(), "nobody-declares-this") {
		t.Fatalf("got %v", err)
	}
}

func TestCollectAndObserve(t *testing.T) {
	s := newSetup(t, "observer")
	set := s.set(t)
	cov := t.TempDir()

	if err := set.Collect(t.Context(), plugin.CollectSliceEnd, "x", filepath.Join(cov, "x"), false); err != nil {
		t.Fatal(err)
	}

	part := filepath.Join(cov, "x")

	obs, err := set.Observe(t.Context(), s.r.Dir, "x", filepath.Join(cov, "_setup"), []string{part})
	if err != nil {
		t.Fatal(err)
	}

	if len(obs) != 1 || obs[0].CantTell || !slices.Equal(obs[0].Touched, []string{"t:a.txt"}) {
		t.Fatalf("observed %+v", obs)
	}

	obs, err = set.Observe(t.Context(), s.r.Dir, "x", "", []string{part})
	if err != nil || len(obs) != 1 || !slices.Equal(obs[0].Touched, []string{"t:a.txt"}) {
		t.Fatalf("without a setup: %+v %v", obs, err)
	}

	obs, err = set.Observe(t.Context(), s.r.Dir, "y", filepath.Join(cov, "_setup"), []string{filepath.Join(cov, "y")})
	if err != nil || len(obs) != 1 || !obs[0].CantTell {
		t.Fatalf("no evidence: %+v %v", obs, err)
	}
}

func TestObserveAndCollectCarryTheClaims(t *testing.T) {
	s := newSetup(t, "observer")
	s.cfg.Plugins[1].Include = []string{"texts/a.txt", "texts/b.txt"}
	set := s.set(t)

	claimed, err := set.Claims(t.Context(), s.r.Dir)
	if err != nil {
		t.Fatal(err)
	}

	set.SetClaimed(claimed)

	cov := t.TempDir()
	part := filepath.Join(cov, "x")

	if err := set.Collect(t.Context(), plugin.CollectSliceEnd, "x", part, false); err != nil {
		t.Fatal(err)
	}

	if got := readFileT(t, filepath.Join(part, "uses", "claimed")); got != "texts/a.txt\ntexts/b.txt\n" {
		t.Errorf("collect was told it claims %q", got)
	}

	obs, err := set.Observe(t.Context(), s.r.Dir, "x", filepath.Join(cov, "_setup"), []string{part})
	if err != nil {
		t.Fatal(err)
	}

	if len(obs) != 1 || !slices.Equal(obs[0].Touched, []string{"t:a.txt", "file:texts/a.txt", "file:texts/b.txt"}) {
		t.Fatalf("observed %+v", obs)
	}
}

func readFileT(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// TestCollectOnlyWhatAPluginHandles: a plugin that declares only slice-end
// is never started for the other collect actions.
func TestCollectOnlyWhatAPluginHandles(t *testing.T) {
	s := newSetup(t, "endonly")
	set := s.set(t)

	for _, action := range []string{plugin.CollectSetupEnd, plugin.CollectSliceStart} {
		dir := t.TempDir()
		if err := set.Collect(t.Context(), action, "s", dir, false); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(dir, "uses", "touched")); err == nil {
			t.Errorf("%s: called", action)
		}
	}

	dir := t.TempDir()
	if err := set.Collect(t.Context(), plugin.CollectSliceEnd, "s", dir, false); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "uses", "touched")); err != nil {
		t.Errorf("slice-end: not called: %v", err)
	}
}

func TestDescribedPluginsAreNotStartedToDescribe(t *testing.T) {
	s := newSetup(t, "observer")
	described := s.set(t).Descriptions()

	set, err := Load(t.Context(), Options{Config: s.cfg, Dir: t.TempDir(), Cache: t.TempDir(), Answers: s.cache, Work: t.TempDir(), Described: described})
	if err != nil {
		t.Fatalf("load from the kept descriptions, with no executables: %v", err)
	}

	if len(set.Plugins) != 2 || set.Plugins[1].Desc.Name != "uses" {
		t.Fatalf("plugins %+v", set.Plugins)
	}
}

func TestAPluginThatClaimsNothingIsWarnedOf(t *testing.T) {
	s := newSetup(t, "links")
	s.cfg.Plugins[1].Include = []string{"nowhere/*.txt"}
	set := s.set(t)

	if _, err := set.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
		t.Fatal(err)
	}

	if ws := set.Report().Warnings; !slices.ContainsFunc(ws, func(w string) bool { return strings.HasPrefix(w, "uses claims nothing in the tree") }) {
		t.Fatalf("warnings %v", ws)
	}
}

func appendLine(file, line string) error {
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the test's own log
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}

	_, werr := f.WriteString(line + "\n")

	return errors.Join(werr, f.Close())
}

func TestARestBearingReAskCountsAsAsked(t *testing.T) {
	s := newSetup(t, "rest")

	log := filepath.Join(t.TempDir(), "calls.log")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	script := "#!/bin/sh\nFAKE=rest FAKE_LOG='" + log + "' exec '" + exe + "' \"$@\"\n"
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(s.dir, "uses"), []byte(script)); err != nil {
		t.Fatal(err)
	}

	plan := func() (CallCount, []string) {
		t.Helper()

		before := strings.Count(readFileT(t, log), "\n")

		set := s.set(t)
		if _, err := set.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
			t.Fatal(err)
		}

		var asked []string

		for _, line := range strings.Split(strings.TrimSpace(readFileT(t, log)), "\n")[before:] {
			asked = append(asked, strings.Fields(line)...)
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "uses" && c.Call == plugin.CallLinks {
				return c, asked
			}
		}

		t.Fatal("no links in the report")

		return CallCount{}, nil
	}

	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	first, _ := plan()
	if first.Cached != 0 {
		t.Fatalf("a first plan from the cache: %+v", first)
	}

	s.r.Write("texts/a.txt", "a changed\n")
	s.r.Write("rest-on", "")
	s.r.Commit("rest")

	for i := range 2 {
		c, asked := plan()

		final := len(asked)
		if i == 0 {
			final = c.Asked + c.Cached
		}

		if c.Asked != final || c.Cached != 0 {
			t.Errorf("plan %d: report %+v, the plugin was sent %d paths in all (%v)", i+2, c, len(asked), asked)
		}
	}
}

func TestAChangeToAnUnclaimedInputAsksTheSetAgain(t *testing.T) {
	s := newSetup(t, "chain")
	s.r.Write("texts/a.txt", "texts/b.txt\n")
	s.r.Write("texts/b.txt", "texts/doc.txt\n")
	s.r.Write("texts/doc.txt", "b.txt\n")
	s.r.Commit("a chain")

	links := func(commit string) ([]plugin.Link, CallCount) {
		t.Helper()

		set := s.set(t)

		tree, err := set.Tree(t.Context(), s.r.Dir, commit, 2)
		if err != nil {
			t.Fatal(err)
		}

		var got []plugin.Link

		for _, l := range tree.Links {
			if l.From == "t:a.txt" {
				got = append(got, l)
			}
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "uses" && c.Call == plugin.CallLinks {
				return got, c
			}
		}

		return got, CallCount{}
	}

	if got, _ := links("c1"); len(got) != 1 || got[0].To != "t:b.txt" {
		t.Fatalf("first: %+v", got)
	}

	if _, c := links("c2"); c.Asked != 0 || c.Cached != 1 {
		t.Fatalf("unchanged: %+v", c)
	}

	s.r.Write("texts/doc.txt", "a.txt\n")
	s.r.Commit("the third file")

	if got, c := links("c3"); c.Asked != 1 || len(got) != 1 || got[0].To != "t:a.txt" {
		t.Fatalf("after the third file changed: %+v %+v", got, c)
	}

	claims, err := s.set(t).Claims(t.Context(), s.r.Dir)
	if err != nil {
		t.Fatal(err)
	}

	want := []plugin.Claim{{Path: "texts/a.txt", Role: "source"}, {Path: "texts/b.txt", Role: "fragment"}, {Path: "texts/doc.txt", Role: "fragment"}}
	if got := claims["uses"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("claims %+v, want %+v", got, want)
	}
}

func TestASetInputOutsideTheTreeIsBoundWide(t *testing.T) {
	s := newSetup(t, "chainout")
	s.r.Write("texts/a.txt", "texts/b.txt\n")
	s.r.Write("texts/b.txt", "texts/doc.txt\n")
	s.r.Commit("a chain")

	for _, commit := range []string{"c1", "c2"} {
		set := s.set(t)

		tree, err := set.Tree(t.Context(), s.r.Dir, commit, 2)
		if err != nil {
			t.Fatal(err)
		}

		if got := tree.Untold["uses"]; !slices.Contains(got, "texts/a.txt") || !slices.Contains(got, "texts/b.txt") {
			t.Fatalf("%s: untold %v", commit, tree.Untold)
		}

		const want = "uses links: the set of texts/a.txt declares ../tsconfig.json, which is outside the tree: bound wide until uses declares paths of the tree"
		if r := set.Report(); !slices.Contains(r.Warnings, want) {
			t.Fatalf("%s: warnings %q", commit, r.Warnings)
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "uses" && c.Call == plugin.CallLinks && c.Asked != 1 {
				t.Fatalf("%s: %+v in %+v", commit, c, set.Report().Calls)
			}
		}
	}
}

func TestAnAbsentInputKeysTheSet(t *testing.T) {
	s := newSetup(t, "chainabsent")
	s.r.Write("texts/a.txt", "texts/b.txt\n")
	s.r.Write("texts/b.txt", "texts/doc.txt\n")
	s.r.Write("texts/doc.txt", "b.txt\n")
	s.r.Commit("a chain")

	plan := func(commit string) (*deps.Tree, CallCount) {
		t.Helper()

		set := s.set(t)

		tree, err := set.Tree(t.Context(), s.r.Dir, commit, 2)
		if err != nil {
			t.Fatal(err)
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "uses" && c.Call == plugin.CallLinks {
				return tree, c
			}
		}

		return tree, CallCount{}
	}

	if tree, c := plan("c1"); c.Asked != 1 || len(tree.Untold) != 0 {
		t.Fatalf("first: %+v %v", c, tree.Untold)
	}

	if _, c := plan("c2"); c.Asked != 0 || c.Cached != 1 {
		t.Fatalf("unchanged: %+v", c)
	}

	s.r.Write("texts/tsconfig.json", "doc.txt\n")
	s.r.Commit("the probed config appears")

	tree, c := plan("c3")
	if c.Asked != 1 || !slices.ContainsFunc(tree.Links, func(l plugin.Link) bool { return l.From == "t:a.txt" && l.To == "t:doc.txt" }) {
		t.Fatalf("after the config appeared: %+v %v", c, tree.Links)
	}
}

func TestFilesWithOneContentAreEachClaimed(t *testing.T) {
	s := newSetup(t, "links")

	copies := []string{"texts/c1.txt", "texts/c2.txt", "texts/c3.txt", "texts/c4.txt", "texts/c5.txt", "texts/c6.txt"}
	for _, p := range copies {
		s.r.Write(p, "one content\n")
	}

	s.r.Commit("copies")

	for _, commit := range []string{"cold", "warm"} {
		set := s.set(t)

		claims, err := set.Claims(t.Context(), s.r.Dir)
		if err != nil {
			t.Fatal(err)
		}

		tree, err := set.Tree(t.Context(), s.r.Dir, commit, 2)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range copies {
			if !slices.ContainsFunc(claims["texts"], func(c plugin.Claim) bool { return c.Path == p }) {
				t.Errorf("%s: %s unclaimed: %+v", commit, p, claims["texts"])
			}

			if id := "t:" + strings.TrimPrefix(p, "texts/"); tree.Units[id].File != p {
				t.Errorf("%s: no unit %s of %s", commit, id, p)
			}
		}
	}
}

func TestACannotTellIsNamedForThePluginThatSaidIt(t *testing.T) {
	s := newSetup(t, "typechecks")

	tree, err := s.set(t).Tree(t.Context(), s.r.Dir, "c", 2)
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[string][2]string{
		"t:a.txt": {"uses", "a.txt does not type-check"},
		"t:bad":   {"texts", "takes its digest from t:missing, which is missing"},
	} {
		if got := [2]string{tree.CannotTellBy[id], tree.CannotTell[id]}; got != want {
			t.Errorf("%s: said by %q: %q, want %q: %q", id, got[0], got[1], want[0], want[1])
		}
	}

	if tree.Providers["t:a.txt"] != "texts" {
		t.Errorf("t:a.txt is listed by %q, want texts", tree.Providers["t:a.txt"])
	}
}

func TestAnAnswerIsKeptForTheBinaryThatGaveIt(t *testing.T) {
	s := newSetup(t, "links")

	for i, p := range s.cfg.Plugins {
		sum, err := FileSHA256(filepath.Join(s.dir, p.Name))
		if err != nil {
			t.Fatal(err)
		}

		s.cfg.Plugins[i].Release = &config.Release{Version: "v0", SHA256: sum}
	}

	units := func() (CallCount, []NotPinned) {
		t.Helper()

		set := s.set(t)
		if _, err := set.Tree(t.Context(), s.r.Dir, "c", 2); err != nil {
			t.Fatal(err)
		}

		for _, c := range set.Report().Calls {
			if c.Plugin == "texts" && c.Call == plugin.CallUnits {
				return c, set.NotPinned()
			}
		}

		t.Fatal("no units call")

		return CallCount{}, nil
	}

	if c, np := units(); c.Asked != 3 || c.Cached != 0 || len(np) != 0 {
		t.Fatalf("first plan: %+v, not pinned %+v", c, np)
	}

	if c, _ := units(); c.Asked != 0 || c.Cached != 3 {
		t.Fatalf("second plan, same binary: %+v", c)
	}

	exe := filepath.Join(s.dir, "texts")
	pinned := readFileT(t, exe)
	built := strings.Replace(pinned, "\n", "\n# built on this machine\n", 1)

	if err := testenv.WriteExecutable(t.Context(), exe, []byte(built)); err != nil {
		t.Fatal(err)
	}

	found, err := FileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}

	c, np := units()
	if c.Asked != 3 || c.Cached != 0 {
		t.Errorf("another binary was served the pinned build's answers: %+v", c)
	}

	want := s.cfg.Plugins[0].Release.SHA256
	if len(np) != 1 || np[0].Plugin != "texts" || np[0].Found != found || np[0].Want != want ||
		!strings.Contains(np[0].Line(), "texts") || !strings.Contains(np[0].Line(), "(v0, sha256 "+want+")") || !strings.Contains(np[0].Line(), found) {
		t.Errorf("not pinned: %+v", np)
	}

	if err := testenv.WriteExecutable(t.Context(), exe, []byte(pinned)); err != nil {
		t.Fatal(err)
	}

	if c, np := units(); c.Asked != 0 || c.Cached != 3 || len(np) != 0 {
		t.Errorf("the pinned binary back: %+v, not pinned %+v", c, np)
	}
}
