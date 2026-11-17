package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeTables lists a unit per table an up migration creates, with no anchor; down files have none.
func fakeTables(describe func(plugin.Description) plugin.Handler) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindSBOM, Name: "tables", Namespaces: []string{"tbl"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Candidates:   []string{"db/*.up.sql"},
			Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerFile},
			Narrowings: []plugin.Narrowing{
				{Name: "migrations-by-table", OffAt: 5, Note: "a migration binds the tables used"},
				{Name: plugins.NarrowSetupSplit, OffAt: 3, Note: "the setup binds its own part"},
			},
		}),
		plugin.CallDetect: func(in []byte) (any, error) {
			var req plugin.DetectRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.DetectAnswer
			for _, p := range req.Paths {
				a.Claims = append(a.Claims, plugin.Claim{Path: p, Role: "migration", ByUnits: "migrations-by-table"})
			}

			return a, nil
		},
		plugin.CallUnits: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.UnitsAnswer

			for _, c := range req.Claimed {
				data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
				if err != nil {
					return nil, fmt.Errorf("units: %w", err)
				}

				for line := range strings.SplitSeq(string(data), "\n") {
					if rest, ok := strings.CutPrefix(line, "CREATE TABLE "); ok {
						table, _, _ := strings.Cut(rest, " ")
						sum := sha256.Sum256([]byte(line))
						a.Units = append(a.Units, plugin.Unit{ID: "tbl:" + table, File: c.Path, Group: c.Path, Digest: hex.EncodeToString(sum[:8])})
					}
				}
			}

			return a, nil
		},
	}
}

// The specs a, b and c open the app through //stack, which holds the migrations two filegroups down
// and the shell; d has no list yet.
func groupsRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("db/BUILD.bazel", `filegroup(name = "migrations", srcs = glob(["*.sql"]))
filegroup(name = "embedded", srcs = [":migrations"], visibility = ["//visibility:public"])
`)
	r.Write("db/0001_users.up.sql", "CREATE TABLE users (id int)\nCREATE TABLE teams (id int)\n")
	r.Write("db/0001_users.down.sql", "DROP TABLE teams\nDROP TABLE users\n")
	r.Write("db/0002_orders.up.sql", "CREATE TABLE orders (id int)\n")
	r.Write("db/0002_orders.down.sql", "DROP TABLE orders\n")
	r.Write("app/BUILD.bazel", `filegroup(name = "shell", srcs = ["BUILD.bazel", "index.html", "config.json"], visibility = ["//visibility:public"])

genrule(name = "config", outs = ["config.json"], cmd = "echo '{}' > $@")

filegroup(name = "tests", srcs = ["one.test.ts"])
`)
	r.Write("app/index.html", "<html></html>\n")
	r.Write("app/one.test.ts", "test('one')\n")
	r.Write("stack/BUILD.bazel", `filegroup(name = "stack", srcs = ["compose.yaml", "//db:embedded", "//app:shell"], visibility = ["//visibility:public"])
`)
	r.Write("stack/compose.yaml", "services: {}\n")

	var specs strings.Builder

	for _, s := range []string{"a", "b", "c", "d"} {
		fmt.Fprintf(&specs, "filegroup(name = %q, srcs = [%q], data = [\"//stack\"])\n", s, s+".spec.ts")
		r.Write("browser/"+s+".spec.ts", "test('"+s+"')\n")
	}

	r.Write("browser/BUILD.bazel", specs.String())
	r.Commit("a browser suite on a stack")

	return r
}

func groupsConfig(t *testing.T, r *testrepo.Repo, level int) *config.Config {
	t.Helper()

	cfg := byUnitsConfig(t, r)
	cfg.Narrowing = level
	browser := cfg.Suites["browser"]
	browser.ExtraDeps = nil
	browser.Sample = 2
	cfg.Suites["browser"] = browser
	cfg.Plugins = []config.Plugin{{Name: "tables", Kind: config.KindSBOM, Repository: "o/tables", Release: cfg.Plugins[1].Release}}

	return cfg
}

var groupsLists = recording.Lists{
	"browser/browser/a.spec.ts": {"tbl:users"},
	"browser/browser/b.spec.ts": {"tbl:teams"},
	"browser/browser/c.spec.ts": {"tbl:orders"},
}

func narrowedBy(p *affected.Plan, narrowing string) []string {
	for _, sp := range p.Suites {
		for _, n := range sp.Narrowed {
			if n.Narrowing == narrowing {
				return n.Dropped
			}
		}
	}

	return nil
}

func browserReasons(p *affected.Plan, name string) []affected.Reason {
	for _, sp := range p.Suites {
		for _, sl := range sp.Selected {
			if sp.Suite == "browser" && sl.Name == name {
				return sp.Reasons[sl.Label]
			}
		}
	}

	return nil
}

func TestATableInAFilegroupSelectsTheSlicesThatUsedIt(t *testing.T) {
	testrepo.Parallel(t)

	r := groupsRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Write("db/0001_users.up.sql", "CREATE TABLE users (id int)\nCREATE TABLE teams (id int, name text)\n")
	head := r.Commit("teams get a name")

	p := planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("level 2: %s", summary(p))

	// d has no list: bound to everything
	if got := browserNames(p); got != "browser/b.spec.ts,browser/d.spec.ts" {
		t.Errorf("a table two filegroups down selects %q, want the spec that used it", got)
	}

	if got := narrowedBy(p, "migrations-by-table"); !slices.Equal(got, []string{"browser/a.spec.ts", "browser/c.spec.ts"}) {
		t.Errorf("migrations-by-table left out %v", got)
	}

	if rs := browserReasons(p, "browser/b.spec.ts"); len(rs) != 1 || rs[0].Kind != affected.KindExecuted || rs[0].What != "tbl:teams" {
		t.Errorf("b: %+v", rs)
	}

	p = planByUnits(t, r, groupsConfig(t, r, 5), groupsLists, base, head)
	t.Logf("level 5: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("with the narrowing off the migration selects %q, want every spec", got)
	}

	// off, nothing is given up, so the stack stays one label as before
	if rs := browserReasons(p, "browser/a.spec.ts"); len(rs) == 0 || !slices.ContainsFunc(rs, func(r affected.Reason) bool {
		return strings.Contains(r.What, "0001_users.up.sql") || slices.Contains(r.Through, "//stack:stack")
	}) {
		t.Errorf("a at level 5: %+v", rs)
	}
}

func TestAFileWithoutUnitsInAFilegroupBindsWhole(t *testing.T) {
	testrepo.Parallel(t)

	r := groupsRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Write("db/0001_users.down.sql", "DROP TABLE teams CASCADE\nDROP TABLE users\n")
	head := r.Commit("a down migration")

	p := planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("down: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("a down file selects %q, want every spec", got)
	}

	want := []string{"//db:migrations", "//db:embedded", "//stack:stack"}
	if rs := browserReasons(p, "browser/a.spec.ts"); len(rs) != 1 || rs[0].Kind != affected.KindTarget ||
		rs[0].What != "//db:0001_users.down.sql" || !slices.Equal(rs[0].Through, want) {
		t.Errorf("a: %+v, want the down file through %v", rs, want)
	}
}

func TestAMigrationAddedToAFilegroupReachesTheSetup(t *testing.T) {
	testrepo.Parallel(t)

	r := groupsRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Write("db/0003_tags.up.sql", "CREATE TABLE tags (id int)\n")
	head := r.Commit("a new table")

	withSetup := recording.Lists{"browser/" + recording.SetupList: {"tbl:users", "tbl:teams", "tbl:orders"}}
	maps.Copy(withSetup, groupsLists)

	p := planByUnits(t, r, groupsConfig(t, r, 2), withSetup, base, head)
	t.Logf("split: %s", summary(p))

	// the setup part is reached, so the suite runs its sample of two, the listless d among them
	if got, left := browserNames(p), narrowedBy(p, plugins.NarrowSetupSplit); strings.Count(got, ",") != 1 || !strings.Contains(got, "browser/d.spec.ts") || len(left) != 2 {
		t.Errorf("a new up migration selects %q, the setup split left out %v: want the setup's sample", got, left)
	}

	p = planByUnits(t, r, groupsConfig(t, r, 3), withSetup, base, head)
	t.Logf("no split: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("without the setup split a new up migration selects %q, want every spec", got)
	}

	if rs := browserReasons(p, "browser/c.spec.ts"); len(rs) != 1 || rs[0].Kind != affected.KindInputAdded || rs[0].What != "//db:0003_tags.up.sql" {
		t.Errorf("c: %+v", rs)
	}

	// no setup list to take it: the file stays whole for every slice
	p = planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("no setup list: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("with no setup list a new up migration selects %q, want every spec", got)
	}
}

func TestAMigrationRenamedInAFilegroupKeepsItWhole(t *testing.T) {
	testrepo.Parallel(t)

	r := groupsRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Git("mv", "db/0002_orders.up.sql", "db/0003_orders.up.sql")
	head := r.Commit("orders after the rest")

	withSetup := recording.Lists{"browser/" + recording.SetupList: {"tbl:users", "tbl:teams", "tbl:orders"}}
	maps.Copy(withSetup, groupsLists)

	p := planByUnits(t, r, groupsConfig(t, r, 2), withSetup, base, head)
	t.Logf("renamed: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("a renamed migration selects %q, want every spec: the order it runs in changed", got)
	}
}

func TestABuildFileInAShellIsNoDataFile(t *testing.T) {
	testrepo.Parallel(t)

	r := groupsRepo(t)
	base := r.Git("rev-parse", "HEAD")
	r.Write("app/two.test.ts", "test('two')\n")
	r.Write("app/BUILD.bazel", `filegroup(name = "shell", srcs = ["BUILD.bazel", "index.html", "config.json"], visibility = ["//visibility:public"])

genrule(name = "config", outs = ["config.json"], cmd = "echo '{}' > $@")

filegroup(name = "tests", srcs = ["one.test.ts", "two.test.ts"])
`)
	head := r.Commit("a unit test listed")

	p := planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("listed: %s", summary(p))

	if got := browserNames(p); got != "browser/d.spec.ts" {
		t.Errorf("a test added to the app's lists selects %q, want only the spec with no list", got)
	}

	if got := narrowedBy(p, plugins.NarrowBuildFiles); !slices.Equal(got, []string{"browser/a.spec.ts", "browser/b.spec.ts", "browser/c.spec.ts"}) {
		t.Errorf("%s left out %v", plugins.NarrowBuildFiles, got)
	}

	p = planByUnits(t, r, groupsConfig(t, r, 5), groupsLists, base, head)
	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("at level 5 the BUILD file selects %q, want every spec", got)
	}

	// lines above a rule of the shell and a sibling target move nothing the shell holds
	base = head

	r.Write("app/BUILD.bazel", `# the app's shell and its tests

filegroup(name = "assets", srcs = [])

filegroup(name = "shell", srcs = ["BUILD.bazel", "index.html", "config.json"], visibility = ["//visibility:public"])

genrule(name = "config", outs = ["config.json"], cmd = "echo '{}' > $@")

filegroup(name = "tests", srcs = ["one.test.ts", "two.test.ts"])
`)
	head = r.Commit("a comment and a sibling")

	p = planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("moved: %s", summary(p))

	if got := browserNames(p); got != "browser/d.spec.ts" {
		t.Errorf("moving the shell's rules selects %q, want only the spec with no list", got)
	}

	// a rule's attribute still selects, through bazel-diff's hash of the rule
	base = head

	r.Write("app/BUILD.bazel", `# the app's shell and its tests

filegroup(name = "assets", srcs = [])

filegroup(name = "shell", srcs = ["BUILD.bazel", "index.html", "config.json"], visibility = ["//visibility:public"])

genrule(name = "config", outs = ["config.json"], cmd = "echo '{\"debug\": true}' > $@")

filegroup(name = "tests", srcs = ["one.test.ts", "two.test.ts"])
`)
	head = r.Commit("the config's command")

	p = planByUnits(t, r, groupsConfig(t, r, 2), groupsLists, base, head)
	t.Logf("rule: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts" {
		t.Errorf("a changed rule in the shell selects %q, want every spec", got)
	}

	if rs := browserReasons(p, "browser/a.spec.ts"); len(rs) != 1 || rs[0].Kind != affected.KindTarget || rs[0].What != "//app:config" ||
		!slices.Equal(rs[0].Through, []string{"//app:shell", "//stack:stack"}) {
		t.Errorf("a: %+v", rs)
	}
}
