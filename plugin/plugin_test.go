package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

var errBroke = errors.New("it broke")

// TestMain lets the test binary act as a plugin when asked, so tests call
// a real executable.
func TestMain(m *testing.M) {
	if os.Getenv("PLUGIN_TEST_SERVE") != "" {
		testenv.ExitChild(m, Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, map[string]Handler{
			CallDescribe: func([]byte) (any, error) {
				return Description{Protocol: Protocol, Kind: KindCorrelator, Name: "fake", Version: "v0",
					Capabilities: []string{CallUnits}, Narrowings: []Narrowing{{Name: "n", OffAt: 2, Note: "x"}}}, nil
			},
			CallUnits: func(in []byte) (any, error) {
				var req TreeRequest
				if err := json.Unmarshal(in, &req); err != nil {
					return nil, fmt.Errorf("request: %w", err)
				}

				if req.Tree == "nothing-here" {
					return nil, ErrCantTell
				}

				lines := `{"id":"x:a","digest":"1"}` + "\n" + `{"id":"x:b","digest":"2","executable":true}` + "\n"
				if err := os.WriteFile(filepath.Join(req.Work, "units.jsonl"), []byte(lines), 0o600); err != nil {
					return nil, fmt.Errorf("units: %w", err)
				}

				return UnitsAnswer{Files: []string{"units.jsonl"}, Units: []Unit{{ID: "x:inline", Digest: "0"}}}, nil
			},
			"env":  func([]byte) (any, error) { return os.Environ(), nil },
			"fail": func([]byte) (any, error) { return nil, errBroke },
			"slow": func([]byte) (any, error) { time.Sleep(5 * time.Second); return Answer{}, nil },
		}))
	}

	os.Exit(m.Run())
}

// fake is the test binary serving as a plugin, through a script that sets
// what a plugin's own environment never carries.
func fake(t *testing.T) Plugin {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "fake")
	if err := testenv.WriteExecutable(t.Context(), script, []byte("#!/bin/sh\nPLUGIN_TEST_SERVE=1 exec '"+self+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	return Plugin{Name: "fake", Path: script, Config: map[string]any{"k": "v"}}
}

func TestAPluginGetsNoToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "the-token-value")
	t.Setenv("PIPELINEGEN_GIT_URL", "https://example.invalid")

	p := fake(t)

	var env []string
	if err := p.Call(t.Context(), "env", p.NewRequest(t.TempDir()), &env); err != nil {
		t.Fatal(err)
	}

	for _, kv := range env {
		if strings.HasPrefix(kv, "GITHUB_TOKEN=") || strings.HasPrefix(kv, "PIPELINEGEN_GIT_URL=") {
			t.Errorf("the plugin got %s", kv)
		}
	}
}

func TestDescribeAndUnits(t *testing.T) {
	p := fake(t)
	work := t.TempDir()

	d, err := p.Describe(t.Context(), work)
	if err != nil || d.Name != "fake" || d.Narrowings[0].OffAt != 2 {
		t.Fatalf("describe: %+v %v", d, err)
	}

	req := TreeRequest{Request: p.NewRequest(work), Tree: "/tree"}

	var ans UnitsAnswer
	if err := p.Call(t.Context(), CallUnits, req, &ans); err != nil {
		t.Fatal(err)
	}

	units, err := ReadLines(work, ans.Files, ans.Units)
	if err != nil {
		t.Fatal(err)
	}

	if len(units) != 3 || units[0].ID != "x:inline" || !units[2].Executable {
		t.Fatalf("units %+v", units)
	}
}

func TestExitCodes(t *testing.T) {
	p := fake(t)
	work := t.TempDir()

	var ans UnitsAnswer

	err := p.Call(t.Context(), CallUnits, TreeRequest{Request: p.NewRequest(work), Tree: "nothing-here"}, &ans)
	if !errors.Is(err, ErrCantTell) || errors.Is(err, ErrFailed) {
		t.Fatalf("can't tell: %v", err)
	}

	err = p.Call(t.Context(), "fail", p.NewRequest(work), nil)
	if !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("failure: %v", err)
	}

	if err := p.Call(t.Context(), "nope", p.NewRequest(work), nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("unknown call: %v", err)
	}

	p.Timeout = 200 * time.Millisecond
	if err := p.Call(t.Context(), "slow", p.NewRequest(work), nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestReadLinesStaysInTheWorkDirectory(t *testing.T) {
	if _, err := ReadLines[Unit](t.TempDir(), []string{"../x"}, nil); err == nil {
		t.Fatal("read outside the work directory")
	}
}

func TestLinkProblems(t *testing.T) {
	for _, c := range []struct {
		link Link
		want string
	}{
		{Link{From: "a", To: "b"}, ""},
		{Link{From: "a", To: "b", Kind: "implements"}, ""},
		{Link{From: "a", To: "b", Kind: LinkDependentsOnly, IfLinked: true}, ""},
		{Link{From: "a", To: "b", Kind: LinkBind, Narrowing: "n", While: WhileOn}, ""},
		{Link{From: "", To: "b"}, "an end is empty"},
		{Link{From: "a", To: "b", While: "maybe"}, `while "maybe"`},
		{Link{From: "a", To: "b", While: WhileOn}, "names none"},
		{Link{From: "a", To: "b", Kind: "dependents_only"}, `did you mean "dependents-only"`},
		{Link{From: "a", To: "b", Kind: "Follow"}, `did you mean "follow"`},
		{Link{From: "a", To: "b", Kind: "USES"}, `did you mean "uses"`},
		{Link{From: "a", To: "b", Kind: LinkDependentsOnly, Ref: true}, ""},
		{Link{From: "a", To: "b", Kind: LinkFollow, Ref: true}, ""},
		{Link{From: "group:a", To: "node:go:test:a", Kind: LinkFollow}, ""},
	} {
		got := c.link.Problem()
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.link, got, c.want)
		}
	}
}

func TestTypedLinksAndTheFlagInJSON(t *testing.T) {
	var l Link
	if err := json.Unmarshal([]byte(`{"from":"a","to":"b","kind":"bind","if_linked":true}`), &l); err != nil {
		t.Fatal(err)
	}

	if !l.Typed() || !l.IfLinked {
		t.Errorf("%+v", l)
	}

	// a link without the flag reads as one that always holds, as records
	// written before it existed do
	var old Link
	if err := json.Unmarshal([]byte(`{"from":"a","to":"b","kind":"uses"}`), &old); err != nil || old.Typed() || old.IfLinked {
		t.Errorf("%+v %v", old, err)
	}

	data, err := json.Marshal(Link{From: "a", To: "b"})
	if err != nil || strings.Contains(string(data), "if_linked") {
		t.Errorf("%s %v", data, err)
	}
}

func TestCoreOwnedIDs(t *testing.T) {
	for id, want := range map[string]bool{"group:internal/notes": true, "node:go:test:a": true, "file:a.go": false, "go:a#api": false} {
		if CoreOwned(id) != want {
			t.Errorf("%s: %v", id, !want)
		}
	}
}

func TestARunnerSaysItsCachesScopes(t *testing.T) {
	data, err := json.Marshal(Description{Kind: KindRunner, CacheScopes: map[string]string{"plan-cache": ScopeJob}})
	if err != nil || !strings.Contains(string(data), `"cache_scopes":{"plan-cache":"job"}`) {
		t.Fatalf("%s %v", data, err)
	}

	if data, err = json.Marshal(Description{Kind: KindCorrelator}); err != nil || strings.Contains(string(data), "cache_scopes") {
		t.Fatalf("%s %v", data, err)
	}
}

func TestAPluginsStderrReachesTheLog(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "react")
	script := "#!/bin/sh\ncat >/dev/null\necho '3 map sources not in the tree' >&2\necho 'done' >&2\necho '{}'\n"

	if err := testenv.WriteExecutable(t.Context(), exe, []byte(script)); err != nil {
		t.Fatal(err)
	}

	var log strings.Builder

	p := Plugin{Name: "react", Path: exe, Log: &log}

	var a Answer
	if err := p.Call(t.Context(), CallCollect, p.NewRequest(t.TempDir()), &a); err != nil {
		t.Fatal(err)
	}

	if want := "react collect: 3 map sources not in the tree\nreact collect: done\n"; log.String() != want {
		t.Fatalf("log %q, want %q", log.String(), want)
	}
}
