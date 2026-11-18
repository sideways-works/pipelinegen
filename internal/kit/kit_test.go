package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// TestMain doubles as the fake plugins: with KIT_FAKE set, the test binary
// serves the call it was started with.
func TestMain(m *testing.M) {
	if kind := os.Getenv("KIT_FAKE"); kind != "" {
		testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, fakes(kind)))
	}

	if kind := os.Getenv("KIT_DRIVER"); kind != "" {
		testenv.ExitChild(m, plugin.ServeDriver(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, fakeDriver(kind)))
	}

	os.Exit(m.Run())
}

func fakes(kind string) map[string]plugin.Handler {
	switch kind {
	case "runner", "runner-more", "runner-ignores", "runner-notices":
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindRunner, Capabilities: []string{plugin.CapPlacement}}),
			plugin.CallPipeline: func(in []byte) (any, error) {
				var req plugin.PipelineRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				// a placement moves the one job; runner-more changes
				// something besides, runner-ignores places nothing
				text := "jobs: " + strconv.Itoa(len(req.Spec.Runs)) + "\n"
				if _, placed := req.Config["placement"]; placed && kind != "runner-ignores" {
					text += "tags: [big]\n"
					if kind == "runner-more" {
						text += "serial: true\n"
					}
				}

				if err := os.WriteFile(filepath.Join(req.Work, "p.yml"), []byte(text), 0o600); err != nil {
					return nil, fmt.Errorf("pipeline: %w", err)
				}

				var a plugin.PipelineAnswer

				a.Files = []string{"p.yml"}

				// a notice says one thing on one line; runner-notices
				// breaks one over two
				a.Notices = []string{"placement: pr-go is placed by no rule"}
				if kind == "runner-notices" {
					a.Notices = append(a.Notices, "two\nlines")
				}

				return a, nil
			},
		}
	case "versioned":
		// the sbom run by a version manager: where the nearest .tool-versions doesn't name its
		// version it marks its answers not cacheable, as a plugin built for another toolchain does
		d := plugin.Description{Kind: plugin.KindSBOM, Namespaces: []string{"t"}, Candidates: []string{"**/*.txt"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Inputs:       map[string]string{plugin.CallUnits: plugin.PerFile, plugin.CallDetect: plugin.PerFile},
			Toolchains:   map[string]string{"fakelang": "fakelang1"}}

		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(d),
			plugin.CallDetect:   fakes("sbom")[plugin.CallDetect],
			plugin.CallUnits: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				a, err := textUnits(in, false)
				if err != nil {
					return nil, err
				}

				out, _ := a.(plugin.UnitsAnswer) //nolint:errcheck // textUnits answers a UnitsAnswer

				data, _ := os.ReadFile(nearestUp(req.Tree, ".tool-versions")) //nolint:errcheck // none is no version
				if !strings.Contains(string(data), "fakelang 1") {
					no := false
					out.Cacheable = &no
				}

				return out, nil
			},
		}
	case "sbom", "sloppy", "nosy", "declared", "declaredout":
		d := plugin.Description{Kind: plugin.KindSBOM, Namespaces: []string{"t"}, Candidates: []string{"**/*.txt"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Inputs:       map[string]string{plugin.CallUnits: plugin.PerFile, plugin.CallDetect: plugin.PerFile}}
		if kind == "declared" || kind == "declaredout" {
			d.Inputs[plugin.CallUnits] = plugin.PerSet
		}

		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(d),
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
			plugin.CallUnits: func(in []byte) (any, error) {
				switch kind {
				case "nosy":
					return nosyUnits(in)
				case "declared", "declaredout":
					return declaredUnits(in, kind == "declaredout")
				}

				return textUnits(in, kind == "sloppy")
			},
		}
	case "binder", "needy", "told":
		d := plugin.Description{Kind: plugin.KindCorrelator, Capabilities: []string{plugin.CallBind}}
		if kind != "binder" {
			d.Needs = []string{"go>=go.mod"}
		}

		if kind == "told" {
			d.Toolchains = map[string]string{"go": "go1.27.1"}
		}

		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(d),
			plugin.CallBind: func(in []byte) (any, error) {
				var req plugin.Request
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				files := map[string]string{
					"units.jsonl":   "\"go:a#func:F\"\n",
					"bind.jsonl":    `{"suite":"s","slice":"a_test.go","width":"executed","units":[[0,"e",-1]]}` + "\n",
					"unbound.jsonl": `{"group":"a","width":"executed","names":[]}` + "\n",
				}

				var a plugin.Answer

				for name, body := range files {
					if err := os.WriteFile(filepath.Join(req.Work, name), []byte(body), 0o600); err != nil {
						return nil, fmt.Errorf("bind: %w", err)
					}

					a.Files = append(a.Files, name)
				}

				slices.Sort(a.Files)

				return a, nil
			},
		}
	case "shifty":
		// a.txt's set links b to the readme only while a.txt ends in a
		// newline: a link that changes while neither end does
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator,
				Capabilities: []string{plugin.CallLinks}, Inputs: map[string]string{plugin.CallLinks: plugin.PerSet}}),
			plugin.CallLinks: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				data, err := os.ReadFile(filepath.Join(req.Tree, "a.txt"))
				if err != nil {
					return nil, fmt.Errorf("links: %w", err)
				}

				set := plugin.Set{Group: "a.txt", Inputs: []string{"a.txt"}, Links: []plugin.Link{{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkFollow}}}
				if strings.HasSuffix(string(data), "\n") {
					set.Links = append(set.Links, plugin.Link{From: "t:b.txt", To: "file:readme.md", Kind: plugin.LinkUses})
				}

				return plugin.LinksAnswer{Sets: []plugin.Set{set}}, nil
			},
		}
	case "importer", "leaky":
		// b.txt's set names a.txt's api unit as an input, not a.txt; the leaky one's link kind depends
		// on a.txt's body all the same, which the api unit's digest doesn't cover
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator,
				Capabilities: []string{plugin.CallLinks}, Inputs: map[string]string{plugin.CallLinks: plugin.PerSet}}),
			plugin.CallLinks: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				data, err := os.ReadFile(filepath.Join(req.Tree, "a.txt"))
				if err != nil {
					return nil, fmt.Errorf("links: %w", err)
				}

				linkKind := plugin.LinkFollow
				if kind == "leaky" && strings.Contains(string(data), "body 2") {
					linkKind = plugin.LinkBind
				}

				var a plugin.LinksAnswer

				for _, set := range []plugin.Set{
					{Group: "a.txt", Inputs: []string{"a.txt"}},
					{Group: "b.txt", Inputs: []string{"b.txt"}, InputUnits: []string{"t:a.txt#api"},
						Links: []plugin.Link{{From: "t:b.txt", To: "t:a.txt", Kind: linkKind}}},
				} {
					if len(req.Paths) == 0 || slices.Contains(req.Paths, set.Group) {
						a.Sets = append(a.Sets, set)
					}
				}

				return a, nil
			},
		}
	case "misspelt":
		// a kind spelt unlike the one it means: a plain link, followed
		// further than meant
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator,
				Capabilities: []string{plugin.CallLinks}, Inputs: map[string]string{plugin.CallLinks: plugin.PerSet}}),
			plugin.CallLinks: func([]byte) (any, error) {
				return plugin.LinksAnswer{Sets: []plugin.Set{{Group: "a.txt", Inputs: []string{"a.txt"},
					Links: []plugin.Link{{From: "t:a.txt", To: "t:b.txt", Kind: "Dependents_Only"}}}}}, nil
			},
		}
	case "sharing", "partial":
		// links in per-file sets that share b.txt, as sets that all read
		// one contract do
		return map[string]plugin.Handler{
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator,
				Capabilities: []string{plugin.CallLinks}, Inputs: map[string]string{plugin.CallLinks: plugin.PerSet}}),
			plugin.CallLinks: func(in []byte) (any, error) {
				var req plugin.TreeRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				sets := []plugin.Set{
					{Group: "a.txt", Inputs: []string{"a.txt", "b.txt"}, Links: []plugin.Link{{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkUses}}},
					{Group: "b.txt", Inputs: []string{"b.txt"}, Links: []plugin.Link{{From: "t:b.txt", To: "t:a.txt", Kind: plugin.LinkUses}}},
				}

				var a plugin.LinksAnswer

				for _, set := range sets {
					holds := slices.ContainsFunc(set.Inputs, func(p string) bool { return slices.Contains(req.Paths, p) })

					// a partial plugin answers only the set a path is named for
					if kind == "partial" {
						holds = slices.Contains(req.Paths, set.Group)
					}

					if len(req.Paths) == 0 || holds {
						a.Sets = append(a.Sets, set)
					}
				}

				return a, nil
			},
		}
	case "correlator", "forgetful", "onepart", "modes":
		return map[string]plugin.Handler{
			// it collects only at a slice's end: the kit calls nothing else
			plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator,
				Capabilities: []string{plugin.CallLinks, plugin.CallObserve, plugin.CallCollect},
				Collects:     []string{plugin.CollectSliceEnd}}),
			plugin.CallLinks: func([]byte) (any, error) {
				return plugin.LinksAnswer{Links: []plugin.Link{
					{From: "t:a.txt", To: "t:b.txt", Kind: plugin.LinkUses},
					{From: "file:a.txt", To: "t:a.txt", Kind: "changes"},
				}}, nil
			},
			plugin.CallObserve: func(in []byte) (any, error) {
				var req plugin.ObserveRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				// a part with no files is no evidence of a recording
				for _, d := range req.Dirs[1:] {
					if entries, err := os.ReadDir(d); err != nil || len(entries) == 0 {
						return nil, plugin.ErrCantTell
					}
				}

				return observeTexts(req, kind == "forgetful")
			},
			plugin.CallCollect: func(in []byte) (any, error) {
				var req plugin.CollectRequest
				if err := decode(in, &req); err != nil {
					return nil, err
				}

				if req.Action != plugin.CollectSliceEnd {
					return nil, fmt.Errorf("%w: collect called for %s, which it never declared", errUndeclared, req.Action)
				}

				// what the slice ran, from the stack into the core's directory; onepart puts a later part
				// where the first went, as a plugin that ignores the core's directories would
				if kind == "onepart" && strings.Contains(filepath.ToSlash(req.Dir), "/.pipelinegen/part-") {
					return plugin.Answer{}, nil
				}

				if err := os.MkdirAll(req.Dir, 0o750); err != nil {
					return nil, fmt.Errorf("collect: %w", err)
				}

				// modes notes how the batch's step ran it: from where, into
				// which name's directory, with which stack and config
				if log, ok := req.Config["log"].(string); ok && kind == "modes" {
					_, src := os.Stat(plugin.ObserveTree)
					line := fmt.Sprintf("%t %s %t %v\n", src == nil, filepath.Base(req.Dir), req.Stack, req.Config["mode"])

					f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						return nil, fmt.Errorf("collect: %w", err)
					}

					_, _ = f.WriteString(line) //nolint:errcheck // the test reads it
					_ = f.Close()              //nolint:errcheck // the test reads it
				}

				if err := os.WriteFile(filepath.Join(req.Dir, "paths.lst"), []byte("a.txt\n"), 0o600); err != nil {
					return nil, fmt.Errorf("collect: %w", err)
				}

				return plugin.Answer{}, nil
			},
		}
	}

	if strings.HasPrefix(kind, "connector") {
		return connectorFakes(kind)
	}

	if kind == "stepreader" {
		return stepReaderFakes()
	}

	if kind == "executor" {
		return executorFakes()
	}

	if kind == "recorder" || kind == "recorderbad" {
		return recorderFakes(kind == "recorderbad")
	}

	if kind == "eraser" || kind == "eraserboth" {
		return eraserFakes(kind == "eraserboth")
	}

	if kind == "execsbom" {
		return execSBOMFakes()
	}

	if strings.HasPrefix(kind, "fill") {
		return fillFakes(kind)
	}

	return nil
}

var errUndeclared = errors.New("an undeclared call")

// observeTexts touches the text unit of each path a recording lists (paths.lst per part) that the tree
// has; none of them can't be told, unless forgetful answers an empty list.
func observeTexts(req plugin.ObserveRequest, forgetful bool) (any, error) {
	touched, listed := []string{}, 0

	for _, d := range req.Dirs[1:] {
		data, err := os.ReadFile(filepath.Join(d, "paths.lst"))
		if err != nil {
			continue
		}

		for p := range strings.FieldsSeq(string(data)) {
			listed++

			if _, err := os.Stat(filepath.Join(req.Tree, p)); err == nil {
				touched = append(touched, "t:"+p)
			}
		}
	}

	if listed > 0 && len(touched) == 0 && !forgetful {
		return nil, plugin.ErrCantTell
	}

	return plugin.ObserveAnswer{Touched: touched}, nil
}

func decode(in []byte, v any) error {
	if err := json.Unmarshal(in, v); err != nil {
		return fmt.Errorf("request: %w", err)
	}

	return nil
}

func describe(d plugin.Description) plugin.Handler {
	d.Protocol, d.Name, d.Version = plugin.Protocol, "fake-"+d.Kind, "v0"

	return func([]byte) (any, error) { return d, nil }
}

// nosyUnits answers a unit whose digest is a file it never claimed: the
// readme, which only *.txt candidates should bring.
func nosyUnits(in []byte) (any, error) {
	var req plugin.TreeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(req.Tree, "readme.md"))
	if err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	sum := sha256.Sum256(data)

	return plugin.UnitsAnswer{Units: []plugin.Unit{{ID: "t:readme", File: "a.txt", Digest: hex.EncodeToString(sum[:8])}}}, nil
}

// declaredUnits reads the readme as nosyUnits does but declares it as an input, so the core keys the
// answer on it; out also declares a file the fixture lacks.
func declaredUnits(in []byte, out bool) (any, error) {
	var req plugin.TreeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(req.Tree, "readme.md"))
	if err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	sum := sha256.Sum256(data)
	set := plugin.Set{Group: "a.txt", Inputs: []string{"a.txt", "readme.md"},
		Units: []plugin.Unit{{ID: "t:readme", File: "a.txt", Digest: hex.EncodeToString(sum[:8])}}}

	set.Inputs = append(set.Inputs, "probed.json")
	set.Absent = []string{"probed.json"}

	if out {
		set.Inputs = append(set.Inputs, "missing.md")
	}

	return plugin.UnitsAnswer{Sets: []plugin.Set{set}}, nil
}

// textUnits is one unit per text file; sloppy answers differ per call and
// name a unit outside the namespace.
func textUnits(in []byte, sloppy bool) (any, error) {
	var req plugin.TreeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	var a plugin.UnitsAnswer

	entries, err := os.ReadDir(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	for _, e := range entries {
		rel := e.Name()
		if !strings.HasSuffix(rel, ".txt") || (len(req.Paths) > 0 && !slices.Contains(req.Paths, rel)) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(req.Tree, rel))
		if err != nil {
			return nil, fmt.Errorf("units: %w", err)
		}

		sum := sha256.Sum256(data)
		u := plugin.Unit{ID: "t:" + rel, File: rel, Digest: hex.EncodeToString(sum[:8])}

		// a text that starts "api " has an api unit, whose digest is its
		// first line alone, as a package's declarations without bodies
		if first, _, _ := strings.Cut(string(data), "\n"); strings.HasPrefix(first, "api ") {
			api := sha256.Sum256([]byte(first))
			a.Units = append(a.Units, plugin.Unit{ID: u.ID + "#api", File: rel, Digest: hex.EncodeToString(api[:8])})
		}

		if sloppy {
			u.Digest = strconv.FormatInt(time.Now().UnixNano(), 10)
		}

		a.Units = append(a.Units, u)
	}

	if sloppy {
		a.Units = append(a.Units, plugin.Unit{ID: "elsewhere:x", Digest: "1"})

		return a, nil
	}

	// a well-behaved answer in a work file, as large answers come
	var lines strings.Builder

	for _, u := range a.Units {
		data, err := json.Marshal(u)
		if err != nil {
			return nil, fmt.Errorf("units: %w", err)
		}

		lines.Write(append(data, '\n'))
	}

	if err := os.WriteFile(filepath.Join(req.Work, "units.jsonl"), []byte(lines.String()), 0o600); err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	var out plugin.UnitsAnswer

	out.Files = []string{"units.jsonl"}

	return out, nil
}

func runFake(t *testing.T, kind string) map[string]string {
	t.Helper()

	return runFakeWith(t, kind, true, true)
}

func runFakeWith(t *testing.T, kind string, withTree, withRecording bool) map[string]string {
	t.Helper()

	out := map[string]string{}

	for _, r := range runFakeResults(t, kind, withTree, withRecording) {
		if !r.Skipped {
			out[r.Check] = r.Problem
		}
	}

	return out
}

func runFakeResults(t *testing.T, kind string, withTree, withRecording bool) []Result {
	t.Helper()

	return runFakeBeside(t, kind, "", withTree, withRecording)
}

// fakeExe is a script running the test binary as one of the fakes: a
// plugin's environment never carries the test's, so the script sets it.
func fakeExe(t *testing.T, kind string) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	self := filepath.Join(t.TempDir(), "fake")
	if err := testenv.WriteExecutable(t.Context(), self, []byte("#!/bin/sh\nKIT_FAKE="+kind+" exec '"+exe+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	return self
}

func runFakeBeside(t *testing.T, kind, sbom string, withTree, withRecording bool) []Result {
	t.Helper()

	return Run(t.Context(), fakeOptions(t, kind, sbom, withTree, withRecording))
}

func fakeOptions(t *testing.T, kind, sbom string, withTree, withRecording bool) Options {
	t.Helper()

	self := fakeExe(t, kind)

	tree := t.TempDir()
	for f, body := range map[string]string{
		"a.txt": "a.txt", "b.txt": "b.txt", "readme.md": "readme.md",
		kitFile:               `{"observe": {"slice": "s", "dirs": ["rec/setup", "rec/s"]}, "record": {"slice": "s", "run": ["sh", "-c", "test -f source/a.txt && test -d coverage && test \"$PIPELINEGEN_RECORD\" = true && mkdir -p coverage/$PIPELINEGEN_SLICE && echo $PIPELINEGEN_PART >> coverage/$PIPELINEGEN_SLICE/raw"], "parts": 2}}`,
		"rec/setup/paths.lst": "",
		"rec/s/paths.lst":     "a.txt\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tree, f)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(tree, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// the kit is given the fixture through a symlink: it walks and hands
	// over the real path
	link := filepath.Join(t.TempDir(), "fixture")
	if err := os.Symlink(tree, link); err != nil {
		t.Fatal(err)
	}

	tree = link

	if !withRecording {
		if err := os.Remove(filepath.Join(tree, kitFile)); err != nil {
			t.Fatal(err)
		}
	}

	if !withTree {
		tree = ""
	}

	o := Options{Path: self, Tree: tree, Work: t.TempDir()}
	if sbom != "" {
		o.SBOM = fakeExe(t, sbom)
	}

	return o
}

func TestWellBehavedPluginsPass(t *testing.T) {
	for _, kind := range []string{"runner", "sbom", "correlator", "binder"} {
		t.Run(kind, func(t *testing.T) {
			results := runFake(t, kind)
			if len(results) < 3 {
				t.Fatalf("only %d checks ran: %v", len(results), results)
			}

			want := map[string]string{
				"runner":     "pipeline is the same every time",
				"sbom":       "an answer for some paths agrees with the whole tree's",
				"correlator": "observe can't tell from a recording it can't attribute",
				"binder":     "bind's files are well formed",
			}[kind]

			for _, check := range []string{want, "no answer or message carries the token"} {
				if _, ran := results[check]; !ran {
					t.Errorf("%q didn't run", check)
				}
			}

			for check, problem := range results {
				if problem != "" {
					t.Errorf("%s: %s", check, problem)
				}
			}
		})
	}
}

func TestCachedAnswersAreThePluginsOwn(t *testing.T) {
	for _, kind := range []string{"sbom", "correlator", "sharing"} {
		results := runFake(t, kind)

		problem, ran := results[cachedCheck]
		if !ran || problem != "" {
			t.Errorf("%s: ran %v: %q", kind, ran, problem)
		}
	}
}

func TestAnAnswerAboutAPathHoldsEverySetHoldingIt(t *testing.T) {
	if p, ran := runFake(t, "sharing")[heldCheck]; !ran || p != "" {
		t.Errorf("sharing: ran %v: %q", ran, p)
	}

	if p := runFake(t, "partial")[heldCheck]; !strings.Contains(p, "left out the sets of a.txt") {
		t.Errorf("partial: %q", p)
	}
}

func TestTheCacheChecksSayWhyTheyDidntRun(t *testing.T) {
	skipped := map[string]string{}

	for _, r := range runFakeResults(t, "binder", true, true) {
		if r.Skipped {
			skipped[r.Check] = r.Problem
		}
	}

	for _, check := range []string{cachedCheck, heldCheck} {
		if skipped[check] == "" {
			t.Errorf("%q isn't skipped with a reason: %v", check, skipped)
		}
	}
}

func TestABinderThatReportsNoToolchainIsNoted(t *testing.T) {
	for _, c := range []struct {
		kind  string
		noted bool
	}{{"binder", false}, {"needy", true}, {"told", false}} {
		results := runFake(t, c.kind)

		for check, problem := range results {
			if problem != "" && check != "bind answers can be cached" {
				t.Errorf("%s: %s: %s", c.kind, check, problem)
			}
		}

		if p := results["bind answers can be cached"]; (p != "") != c.noted || c.noted && !strings.Contains(p, "go>=go.mod") {
			t.Errorf("%s: note %q", c.kind, p)
		}
	}
}

func TestAForgetfulCorrelatorFails(t *testing.T) {
	results := runFake(t, "forgetful")

	if results["observe can't tell from a recording it can't attribute"] == "" {
		t.Fatalf("a forgetful correlator passed: %v", results)
	}

	if p := results["observe attributes the fixture's recording"]; p != "" {
		t.Fatalf("the fixture's own recording: %s", p)
	}
}

func TestAPluginReadingWhatItDidntClaimFails(t *testing.T) {
	if results := runFake(t, "nosy"); results["answers depend only on claimed files"] == "" {
		t.Fatalf("a nosy sbom passed: %v", results)
	}

	if results := runFake(t, "sbom"); results["answers depend only on claimed files"] != "" {
		t.Fatalf("a well-behaved sbom failed: %v", results["answers depend only on claimed files"])
	}

	results := runFake(t, "declared")
	if results["answers depend only on claimed files"] != "" || results["a set's inputs are files of the tree"] != "" {
		t.Fatalf("a plugin declaring what it reads failed: %q %q", results["answers depend only on claimed files"], results["a set's inputs are files of the tree"])
	}

	if p := runFake(t, "declaredout")["a set's inputs are files of the tree"]; !strings.Contains(p, "missing.md") || strings.Contains(p, "probed.json") {
		t.Fatalf("an input the fixture lacks, not listed as absent: %q", p)
	}
}

func TestASloppySBOMFails(t *testing.T) {
	results := runFake(t, "sloppy")

	for _, check := range []string{"units are the same every time", "units are well formed"} {
		if results[check] == "" {
			t.Errorf("%s passed: %v", check, results)
		}
	}
}

func TestACheckThatNeverRanFails(t *testing.T) {
	for _, kind := range []string{"sbom", "correlator"} {
		if p := runFakeWith(t, kind, false, true)["the fixture exercises every call the plugin declares"]; !strings.Contains(p, "never ran") {
			t.Errorf("%s without a tree: %q", kind, p)
		}
	}

	if p := runFakeWith(t, "correlator", true, false)["observe attributes the fixture's recording"]; !strings.Contains(p, "names no recording") {
		t.Errorf("a correlator with no recording to observe: %q", p)
	}
}

func TestAMisspeltLinkKindFails(t *testing.T) {
	results := runFake(t, "misspelt")

	if p := results["links are well formed"]; !strings.Contains(p, `did you mean "dependents-only"`) {
		t.Errorf("well formed: %q", p)
	}

	if p := results[cachedCheck]; !strings.Contains(p, "Dependents_Only") {
		t.Errorf("cached: %q", p)
	}
}

func TestLinksThatChangeWhileNeitherEndDoesAreNoted(t *testing.T) {
	find := func(results []Result) (Result, bool) {
		for _, r := range results {
			if r.Check == endsCheck {
				return r, true
			}
		}

		return Result{}, false
	}

	r, ok := find(runFakeBeside(t, "shifty", "sbom", true, false))
	if !ok || !r.Note || !strings.Contains(r.Problem, "came: t:b.txt>file:readme.md") {
		t.Errorf("shifty: %+v", r)
	}

	if r, ok := find(runFakeBeside(t, "sharing", "sbom", true, false)); !ok || r.Problem != "" || r.Note {
		t.Errorf("sharing: %+v", r)
	}

	if r, ok := find(runFakeBeside(t, "sharing", "", true, false)); !ok || !r.Skipped || !strings.Contains(r.Problem, "--sbom") {
		t.Errorf("sharing without an sbom: %+v", r)
	}
}

func TestSetsKeyedOnInputUnits(t *testing.T) {
	edits := `{"edits": [{"path": "a.txt", "old": "body 1", "new": "body 2"}]}`

	for _, c := range []struct {
		kind, sbom, kitFile, want string
		skipped                   bool
	}{
		{"importer", "sbom", edits, "", false},
		{"leaky", "sbom", edits, "the cache served other links", false},
		{"importer", "sbom", `{}`, "names no edits", false},
		{"importer", "", edits, "--sbom", true},
	} {
		tree := t.TempDir()
		for f, body := range map[string]string{"a.txt": "api 1\nbody 1\n", "b.txt": "b\n", kitFile: c.kitFile} {
			if err := os.WriteFile(filepath.Join(tree, f), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		o := Options{Path: fakeExe(t, c.kind), Tree: tree, Work: t.TempDir()}
		if c.sbom != "" {
			o.SBOM = fakeExe(t, c.sbom)
		}

		var got *Result

		for _, r := range Run(t.Context(), o) {
			if r.Check == unitsCheck {
				got = &r
			}
		}

		if got == nil || got.Skipped != c.skipped || (c.want == "") != (got.Problem == "") || !strings.Contains(got.Problem, c.want) {
			t.Errorf("%s with sbom %q and %s: %+v", c.kind, c.sbom, c.kitFile, got)
		}
	}
}

func TestTheKitsCopiesUseTheFixturesToolchain(t *testing.T) {
	for _, pinned := range []bool{true, false} {
		t.Run(fmt.Sprintf("pinned %t", pinned), func(t *testing.T) {
			above := t.TempDir()
			if pinned {
				if err := os.WriteFile(filepath.Join(above, ".tool-versions"), []byte("fakelang 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			tree := filepath.Join(above, "fixture")
			if err := os.MkdirAll(tree, 0o750); err != nil {
				t.Fatal(err)
			}

			for _, f := range []string{"a.txt", "b.txt"} {
				if err := os.WriteFile(filepath.Join(tree, f), []byte(f), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			results := Run(t.Context(), Options{Path: fakeExe(t, "versioned"), Tree: tree, Work: t.TempDir()})

			var copies *Result

			for i, r := range results {
				switch {
				case r.Check == copiesCheck:
					copies = &results[i]
				case r.Problem != "" && !r.Skipped:
					t.Errorf("%s: %s", r.Check, r.Problem)
				}
			}

			switch {
			case copies == nil:
				t.Fatalf("%q didn't run: %v", copiesCheck, results)
			case pinned && copies.Problem != "":
				t.Errorf("pinned: %+v", *copies)
			case !pinned && (!copies.Note || !strings.Contains(copies.Problem, "marked not cacheable") || !strings.Contains(copies.Problem, "fakelang1")):
				t.Errorf("not pinned: %+v", *copies)
			}
		})
	}
}

func TestCompleteNamesClaimedFiles(t *testing.T) {
	claimed := []plugin.Claim{{Path: "a.js", Role: "source"}}

	k := &kit{desc: plugin.Description{Kind: plugin.KindSBOM}}
	k.checkComplete(plugin.UnitsAnswer{Complete: []string{"a.js"}, Sets: []plugin.Set{{Complete: []string{"b.js"}}}}, claimed)

	if len(k.out) != 1 || !strings.Contains(k.out[0].Problem, "b.js is said to be covered whole, but wasn't claimed") || strings.Contains(k.out[0].Problem, "a.js") {
		t.Errorf("sbom: %+v", k.out)
	}

	k = &kit{desc: plugin.Description{Kind: plugin.KindCorrelator}}
	k.checkComplete(plugin.UnitsAnswer{Complete: []string{"a.js"}}, claimed)

	if len(k.out) != 1 || !strings.Contains(k.out[0].Problem, "a correlator says a.js is covered whole") {
		t.Errorf("correlator: %+v", k.out)
	}
}

func TestAnAnchorIsAUnit(t *testing.T) {
	claimed := []plugin.Claim{
		{Path: "c/en.json", Role: "catalog", ByUnits: "keys", Anchor: "loc:c"},
		{Path: "d/en.json", Role: "catalog", ByUnits: "keys", Anchor: "loc:d"},
		{Path: "e/en.json", Role: "catalog", Anchor: "loc:c"},
	}

	k := &kit{desc: plugin.Description{Kind: plugin.KindSBOM}}
	k.checkAnchors(plugin.UnitsAnswer{Units: []plugin.Unit{{ID: "loc:c", Digest: "a"}}}, claimed)

	if len(k.out) != 1 || !strings.Contains(k.out[0].Problem, "d/en.json names the anchor loc:d, which no unit is") ||
		!strings.Contains(k.out[0].Problem, "e/en.json names the anchor loc:c without by_units") || strings.Contains(k.out[0].Problem, "c/en.json") {
		t.Errorf("%+v", k.out)
	}
}

func TestAPlacementMovesStepsAndNothingElse(t *testing.T) {
	for kind, want := range map[string]string{
		"runner":         "",
		"runner-more":    "differ in more than worker tags",
		"runner-ignores": "moved no step",
	} {
		o := fakeOptions(t, kind, "", false, false)
		o.Config = map[string]any{"placement": map[string]any{"default": []any{"big"}}}

		var got *Result

		for _, r := range Run(t.Context(), o) {
			if r.Check == placementCheck {
				got = &r
			}
		}

		switch {
		case got == nil || got.Skipped:
			t.Errorf("%s: the check didn't run: %v", kind, got)
		case want == "" && got.Problem != "", want != "" && !strings.Contains(got.Problem, want):
			t.Errorf("%s: %q, want %q", kind, got.Problem, want)
		}
	}

	for _, r := range Run(t.Context(), fakeOptions(t, "runner", "", false, false)) {
		if r.Check == placementCheck && !r.Skipped {
			t.Errorf("without a placement: %+v", r)
		}
	}
}

func TestARunnersNoticesAreOneLineEach(t *testing.T) {
	for kind, want := range map[string]string{"runner": "", "runner-notices": `"two\nlines"`} {
		var got *Result

		for _, r := range Run(t.Context(), fakeOptions(t, kind, "", false, false)) {
			if r.Check == "the pipeline's notices are one line each" {
				got = &r
			}
		}

		switch {
		case got == nil || got.Skipped:
			t.Errorf("%s: the check didn't run: %v", kind, got)
		case want == "" && got.Problem != "", want != "" && !strings.Contains(got.Problem, want):
			t.Errorf("%s: %q, want %q", kind, got.Problem, want)
		}
	}
}
