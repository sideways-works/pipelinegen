package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

// rulesBinder is a bind correlator whose knowledge is FAKE_RULES
// ("x>y,..."): a slice whose list or roots hold x binds y. Each call goes
// to FAKE_LOG as "<name> <roots of the slice>".
func rulesBinder() map[string]plugin.Handler {
	name := os.Getenv("FAKE_NAME")

	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindCorrelator, Name: name, Capabilities: []string{plugin.CallBind}}),
		plugin.CallBind: func(in []byte) (any, error) {
			var req struct {
				Work   string `json:"work"`
				Slices []struct {
					Suite string   `json:"suite"`
					Name  string   `json:"name"`
					List  []string `json:"list"`
				} `json:"slices"`
				Roots map[string][]string `json:"roots"`
			}
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			var ids, records []string

			for _, sl := range req.Slices {
				roots := slices.Sorted(slices.Values(req.Roots[sl.Name]))
				if err := appendLine(os.Getenv("FAKE_LOG"), name+" "+strings.Join(roots, " ")); err != nil {
					return nil, err
				}

				have := append(slices.Clone(sl.List), roots...)

				var units [][]any

				for rule := range strings.SplitSeq(os.Getenv("FAKE_RULES"), ",") {
					from, to, _ := strings.Cut(rule, ">")
					if slices.Contains(have, from) {
						units = append(units, []any{len(ids), "calls", -1})
						ids = append(ids, to)
					}
				}

				rec, err := json.Marshal(map[string]any{"suite": sl.Suite, "slice": sl.Name, "width": "executed", "units": units})
				if err != nil {
					return nil, fmt.Errorf("bind: %w", err)
				}

				records = append(records, string(rec))
			}

			var lines []string

			for _, id := range ids {
				lines = append(lines, `"`+id+`"`)
			}

			var a plugin.Answer

			for file, content := range map[string][]string{"units.jsonl": lines, "bind.jsonl": records} {
				if err := os.WriteFile(filepath.Join(req.Work, file), []byte(strings.Join(content, "\n")+"\n"), 0o600); err != nil {
					return nil, fmt.Errorf("bind: %w", err)
				}

				a.Files = append(a.Files, file)
			}

			return a, nil
		},
	}
}

func binders(t *testing.T, rules map[string]string) (*Set, string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	set := &Set{o: Options{Work: t.TempDir()}}

	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range rules {
			if !yield(n) {
				return
			}
		}
	}) {
		script := filepath.Join(dir, name)
		body := "#!/bin/sh\nFAKE=rules FAKE_NAME=" + name + " FAKE_RULES='" + rules[name] + "' FAKE_LOG='" + log + "' exec '" + exe + "' \"$@\"\n"

		if err := testenv.WriteExecutable(t.Context(), script, []byte(body)); err != nil {
			t.Fatal(err)
		}

		set.Plugins = append(set.Plugins, &Loaded{
			Conf: config.Plugin{Name: name, Kind: config.KindCorrelator},
			P:    plugin.Plugin{Name: name, Path: script},
			Desc: plugin.Description{Kind: plugin.KindCorrelator, Name: name, Capabilities: []string{plugin.CallBind}},
		})
	}

	return set, log
}

func unitsTree(ids []string, links ...[2]string) *deps.Tree {
	t := &deps.Tree{Units: map[string]deps.Unit{}}

	for _, id := range ids {
		t.Units[id] = deps.Unit{ID: id, Digest: "d"}
	}

	for _, ln := range links {
		t.Links = append(t.Links, plugin.Link{From: ln[0], To: ln[1], Kind: plugin.LinkUses})
	}

	return t
}

func TestABinderIsAskedAgainOnlyForNewRoots(t *testing.T) {
	sl := []BindSlice{{Suite: "api", Name: "a_test.go", List: []string{"a:1"}, HasList: true}}

	t.Run("a lone binder", func(t *testing.T) {
		set, log := binders(t, map[string]string{"go": "a:1>a:2,a:2>a:3"})

		bound, err := set.Bind(t.Context(), t.TempDir(), unitsTree([]string{"a:1", "a:2", "a:3"}), sl)
		if err != nil {
			t.Fatal(err)
		}

		if got := readLog(t, log); !slices.Equal(got, []string{"go a:1"}) {
			t.Fatalf("calls %q, want one", got)
		}

		if units := executed(bound).Units; !slices.Contains(units, "a:2") || slices.Contains(units, "a:3") {
			t.Fatalf("bound %v", units)
		}
	})

	t.Run("two binders feeding each other", func(t *testing.T) {
		// go binds a:2 from a:1, whose link reaches b:1; sql binds b:2
		// from b:1, whose link reaches a:3; go binds a:4 from a:3
		set, log := binders(t, map[string]string{"go": "a:1>a:2,a:3>a:4", "sql": "b:1>b:2"})
		tree := unitsTree([]string{"a:1", "a:2", "a:3", "a:4", "b:1", "b:2"}, [2]string{"a:2", "b:1"}, [2]string{"b:2", "a:3"})

		bound, err := set.Bind(t.Context(), t.TempDir(), tree, sl)
		if err != nil {
			t.Fatal(err)
		}

		want := []string{
			"go a:1",
			"sql a:1 a:2 b:1",
			"go a:1 b:1 b:2 a:3",
			"sql a:1 a:2 a:3 a:4 b:1",
		}

		got := readLog(t, log)
		for i := range got {
			f := strings.Fields(got[i])
			got[i] = f[0] + " " + strings.Join(slices.Sorted(slices.Values(f[1:])), " ")
		}

		for i := range want {
			f := strings.Fields(want[i])
			want[i] = f[0] + " " + strings.Join(slices.Sorted(slices.Values(f[1:])), " ")
		}

		if !slices.Equal(got, want) {
			t.Fatalf("calls\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}

		if units := executed(bound).Units; !slices.Contains(units, "a:4") || !slices.Contains(units, "b:2") {
			t.Fatalf("bound %v", units)
		}
	})
}

func readLog(t *testing.T, log string) []string {
	t.Helper()

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func executed(bound []deps.Slice) deps.Slice {
	for _, d := range bound {
		if d.Width == Executed {
			return d
		}
	}

	return deps.Slice{}
}
