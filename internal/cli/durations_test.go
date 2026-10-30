package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

const durationsConfig = `
pipeline: shop
suites:
  backend:
    tests: ["backend/**/*_test.go"]
    helpers: ["**/helpers_test.go"]
    run_slice: ci/backend.yml
    durations: ci/durations/backend.json
  lint:
    tag: slice-lint
    run_slice: ci/lint.yml
github: {repository: o/r, token: gh}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  main:
    trigger: {branch: main}
    compare: none
    select: all
    suites: {backend: {}}
`

const goJSONRun = `{"Action":"pass","Package":"example.com/b/api","Test":"TestOrders","Elapsed":3}
{"Action":"pass","Package":"example.com/b/api","Test":"TestTotals","Elapsed":2}
{"Action":"pass","Package":"example.com/b/api","Test":"TestVanished","Elapsed":1}
{"Action":"pass","Package":"example.com/b/money","Test":"TestRound","Elapsed":0.5}
`

func TestDurationsCommand(t *testing.T) {
	root := t.TempDir()

	for p, body := range map[string]string{
		"pipelinegen.yaml":            durationsConfig,
		"backend/go.mod":              "module example.com/b\n\ngo 1.27\n",
		"backend/api/orders_test.go":  "package api\n\nimport \"testing\"\n\nfunc TestOrders(t *testing.T) {}\n\nfunc TestTotals(t *testing.T) {}\n",
		"backend/api/refunds_test.go": "package api\n\nimport \"testing\"\n\nfunc TestRefunds(t *testing.T) {}\n",
		"backend/money/money_test.go": "package money\n\nimport \"testing\"\n\nfunc TestRound(t *testing.T) {}\n",
		"ci/durations/backend.json":   `{"version": 1, "files": {"backend/api/refunds_test.go": 9, "backend/api/orders_test.go": 4, "backend/api/gone_test.go": 4}}`,
	} {
		writeFile(t, filepath.Join(root, p), body)
	}

	checkout(t, root, 0)

	report := filepath.Join(t.TempDir(), "go.json")
	writeFile(t, report, goJSONRun)

	stdout, stderr, code := run(t, "durations", "--root", root, "--suite", "backend", "--go-json", report)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	want := "ci/durations/backend.json: 1 added, 1 changed, 1 removed, 3 test files\n" +
		"  + backend/money/money_test.go 0.5s\n" +
		"  ~ backend/api/orders_test.go 5.0s, was 4.0s\n" +
		"  - backend/api/gone_test.go 4.0s\n" +
		"no test file of backend declares these tests the reports timed:\n" +
		"  example.com/b/api.TestVanished\n"
	if stdout != want {
		t.Fatalf("said\n%s\nwant\n%s", stdout, want)
	}

	data, err := os.ReadFile(filepath.Join(root, "ci/durations/backend.json"))
	if err != nil || !strings.Contains(string(data), `"backend/api/orders_test.go": 5,`) || strings.Contains(string(data), "gone_test") {
		t.Fatalf("file: %v\n%s", err, data)
	}

	stdout, _, code = run(t, "durations", "--root", root, "--suite", "backend", "--go-json", report, "--go-json", report)
	if code != 0 || !strings.Contains(stdout, "~ backend/api/orders_test.go 10.0s, was 5.0s") {
		t.Fatalf("two reports add up: %d\n%s", code, stdout)
	}

	for _, args := range [][]string{
		{"durations", "--root", root, "--suite", "backend"},
		{"durations", "--root", root, "--go-json", report},
		{"durations", "--root", root, "--suite", "nope", "--go-json", report},
	} {
		if _, _, code := run(t, args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}

	if _, stderr, code := run(t, "durations", "--root", root, "--suite", "lint", "--go-json", report); code != 1 || !strings.Contains(stderr, "no test files") {
		t.Errorf("tagged suite: %d %s", code, stderr)
	}
}

func TestDurationsFromARunsRefs(t *testing.T) { durationsFromARunsRecords(t) }

func TestDurationsFromARunsRecordsOnABranch(t *testing.T) { onBranch(t, durationsFromARunsRecords) }

func durationsFromARunsRecords(t *testing.T) {
	t.Helper()

	root, _, store := runEnv(t)

	conf := durationsConfig
	if onBranchStore {
		conf += "store: {backend: branch}\n"
	}

	for p, body := range map[string]string{
		"pipelinegen.yaml":            conf,
		"backend/go.mod":              "module example.com/b\n\ngo 1.27\n",
		"backend/api/orders_test.go":  "package api\n\nimport \"testing\"\n\nfunc TestOrders(t *testing.T) {}\n",
		"backend/api/refunds_test.go": "package api\n\nimport \"testing\"\n\nfunc TestRefunds(t *testing.T) {}\n",
	} {
		writeFile(t, filepath.Join(root, p), body)
	}

	checkout(t, root, 0)

	id := report.ID{Run: "main", Change: "main", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), store, id, []byte(`{"suites": []}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Write(t.Context(), "slice", gitstore.Update{Ref: id.StateRef("backend", "backend/api/orders_test.go"), Any: true,
		Files: map[string][]byte{"state.json": []byte(`{"state": "passed", "seconds": 41.26}`)}}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "durations", "--root", root, "--suite", "backend", "--run", "main", "--change", "main", "--param", "race")
	if code != 0 || stdout != "ci/durations/backend.json: 1 added, 0 changed, 0 removed, 1 test file\n  + backend/api/orders_test.go (race 41.3s)\n" {
		t.Fatalf("%d %s\n%s", code, stderr, stdout)
	}

	if _, _, code := run(t, "durations", "--root", root, "--suite", "backend", "--run", "main"); code != 2 {
		t.Fatalf("--run without --change: %d", code)
	}
}
