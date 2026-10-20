package durations

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func recordRepo(extra map[string]string) Repo {
	m := map[string]string{
		"backend/go.mod":              "module example.com/b\n\ngo 1.27\n",
		"backend/api/orders_test.go":  "package api\n\nimport \"testing\"\n\nfunc TestOrders(t *testing.T) {}\n\nfunc TestTotals(t *testing.T) {}\n",
		"backend/api/refunds_test.go": "package api\n\nimport \"testing\"\n\nfunc TestRefunds(t *testing.T) {}\n",
		"backend/api/helpers_test.go": "package api\n\nfunc helper() {}\n",
		"backend/money/money_test.go": "package money\n\nimport \"testing\"\n\nfunc TestRound(t *testing.T) {}\n",
		"e2e/checkout.spec.ts":        "",
		"e2e/console/search.spec.ts":  "",
		"ci/backend.json":             `{"version": 1, "files": {"backend/api/refunds_test.go": 9, "backend/api/gone_test.go": 4}}`,
	}

	maps.Copy(m, extra)

	files := make([]string, 0, len(m))
	for k := range m {
		files = append(files, k)
	}

	slices.Sort(files)

	return Repo{Files: files, Read: func(p string) ([]byte, error) {
		s, ok := m[p]
		if !ok {
			return nil, fs.ErrNotExist
		}

		return []byte(s), nil
	}}
}

func recordCfg() *config.Config {
	return &config.Config{Suites: map[string]config.Suite{
		"backend": {Tests: []string{"backend/**/*_test.go"}, Helpers: []string{"**/helpers_test.go"}, Durations: "ci/backend.json"},
		"e2e":     {Tests: []string{"e2e/**/*.spec.ts"}, Durations: "ci/e2e.json"},
		"lint":    {Tag: "slice-lint"},
	}}
}

const goRun = `{"Action":"pass","Package":"example.com/b/api","Test":"TestOrders","Elapsed":3}
{"Action":"pass","Package":"example.com/b/api","Test":"TestTotals","Elapsed":2}
{"Action":"pass","Package":"example.com/b/api","Test":"TestVanished","Elapsed":1}
{"Action":"pass","Package":"example.com/b/money","Test":"TestRound","Elapsed":0.5}
{"Action":"pass","Package":"example.org/elsewhere","Test":"TestX","Elapsed":1}
`

func TestRecordGoTestsByTheirFiles(t *testing.T) {
	got, err := Record(recordCfg(), recordRepo(nil), "backend", Reports{GoJSON: []io.Reader{strings.NewReader(goRun)}})
	if err != nil {
		t.Fatal(err)
	}

	want := "{\"version\": 1, \"files\": {\n" +
		"  \"backend/api/orders_test.go\": 5,\n" +
		"  \"backend/api/refunds_test.go\": 9,\n" +
		"  \"backend/money/money_test.go\": 0.5\n" +
		"}}\n"
	if got.Path != "ci/backend.json" || string(got.Content) != want {
		t.Fatalf("got %s:\n%s", got.Path, got.Content)
	}

	if !reflect.DeepEqual(got.Unknown, []string{"example.com/b/api.TestVanished", "example.org/elsewhere.TestX"}) {
		t.Fatalf("unknown %v", got.Unknown)
	}
}

//nolint:gosec // a test report, not a credential
const pwRun = `{"config": {"rootDir": "/tmp/build/abc/source/e2e"}, "suites": [
  {"file": "checkout.spec.ts", "specs": [{"file": "checkout.spec.ts", "tests": [
    {"projectName": "phone", "results": [{"duration": 2000}, {"duration": 1000}]}]}]},
  {"file": "console/search.spec.ts", "specs": [{"file": "console/search.spec.ts", "tests": [
    {"projectName": "desktop", "results": [{"duration": 4000}]}]}]}
]}`

func TestRecordPlaywrightFindsItsBase(t *testing.T) {
	got, err := Record(recordCfg(), recordRepo(nil), "e2e", Reports{Playwright: []io.Reader{strings.NewReader(pwRun)}})
	if err != nil {
		t.Fatal(err)
	}

	want := "{\"version\": 1, \"files\": {\n" +
		"  \"e2e/checkout.spec.ts\": {\"phone\": 3},\n" +
		"  \"e2e/console/search.spec.ts\": {\"desktop\": 4}\n" +
		"}}\n"
	if string(got.Content) != want {
		t.Fatalf("got\n%s", got.Content)
	}

	if _, err := Record(recordCfg(), recordRepo(nil), "e2e", Reports{
		Playwright:     []io.Reader{strings.NewReader(pwRun)},
		PlaywrightBase: "elsewhere",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRefusesWhatItCantRecord(t *testing.T) {
	cases := map[string]struct {
		suite string
		rep   Reports
	}{
		"no suite":       {"nope", Reports{}},
		"a tagged suite": {"lint", Reports{}},
		"no base found":  {"e2e", Reports{Playwright: []io.Reader{strings.NewReader(`{"config": {"rootDir": "/x/y"}, "suites": [{"file": "a.spec.ts", "specs": [{"file": "a.spec.ts", "tests": [{"projectName": "p", "results": [{"duration": 1}]}]}]}]}`)}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Record(recordCfg(), recordRepo(nil), c.suite, c.rep); !errors.Is(err, ErrRecord) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRecordTakesMeasuredSeconds(t *testing.T) {
	got, err := Record(recordCfg(), recordRepo(nil), "backend", Reports{Param: "race", Seconds: map[string]float64{
		"backend/api/orders_test.go": 12.34, "backend/api/vanished_test.go": 3,
	}})
	if err != nil {
		t.Fatal(err)
	}

	f, err := Parse(got.Content)
	if err != nil {
		t.Fatal(err)
	}

	if f.Files["backend/api/orders_test.go"]["race"] != 12.3 || f.Files["backend/api/refunds_test.go"][Plain] != 9 {
		t.Fatalf("durations %v", f.Files)
	}

	if _, ok := f.Files["backend/api/vanished_test.go"]; ok {
		t.Fatal("a file the suite doesn't have kept its time")
	}
}
