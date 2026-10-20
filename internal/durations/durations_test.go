package durations

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTakesNumbersAndObjects(t *testing.T) {
	f, err := Parse([]byte(`{"version": 1, "files": {"a_test.go": 7.9, "e2e/b.spec.ts": {"phone": 3, "desktop": 2.5}}}`))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]float64{
		"a_test.go":     {Plain: 7.9},
		"e2e/b.spec.ts": {"phone": 3, "desktop": 2.5},
	}
	if !reflect.DeepEqual(f.Files, want) {
		t.Fatalf("got %v, want %v", f.Files, want)
	}
}

func TestParseRefusesWhatItCantRead(t *testing.T) {
	for name, text := range map[string]string{
		"not json":      `{`,
		"wrong version": `{"version": 2, "files": {}}`,
		"a string":      `{"version": 1, "files": {"a": "7s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(text)); !errors.Is(err, ErrFormat) {
				t.Fatalf("got %v, want ErrFormat", err)
			}
		})
	}
}

func TestMarshalIsOneSortedLinePerFile(t *testing.T) {
	f := New()
	f.Files["z_test.go"] = map[string]float64{Plain: 1.04}
	f.Files["a.spec.ts"] = map[string]float64{"phone": 35, "desktop": 31.25}

	want := "{\"version\": 1, \"files\": {\n" +
		"  \"a.spec.ts\": {\"desktop\": 31.3, \"phone\": 35},\n" +
		"  \"z_test.go\": 1\n" +
		"}}\n"
	if got := string(f.Marshal()); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}

	back, err := Parse(f.Marshal())
	if err != nil {
		t.Fatal(err)
	}

	if back.Files["a.spec.ts"]["desktop"] != 31.3 {
		t.Fatalf("round trip: %v", back.Files)
	}

	if got := string(New().Marshal()); got != "{\"version\": 1, \"files\": {}}\n" {
		t.Fatalf("empty: %q", got)
	}
}

func TestReadOfAMissingFileIsEmpty(t *testing.T) {
	f, err := Read(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(f.Files) != 0 {
		t.Fatalf("got %v, %v", f, err)
	}
}

func TestWriteThenRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.json")
	f := New()
	f.Files["a"] = map[string]float64{Plain: 2}

	if err := f.Write(p); err != nil {
		t.Fatal(err)
	}

	back, err := Read(p)
	if err != nil || back.Files["a"][Plain] != 2 {
		t.Fatalf("got %v, %v", back, err)
	}
}

func TestSecondsSumsTheChosenParams(t *testing.T) {
	f := New()
	f.Files["a"] = map[string]float64{"phone": 3, "desktop": 2, "tablet": 10}

	cases := []struct {
		params []string
		want   float64
		ok     bool
	}{
		{nil, 15, true},
		{[]string{"phone", "desktop"}, 5, true},
		{[]string{"watch"}, 0, false},
	}
	for _, c := range cases {
		got, ok := f.Seconds("a", c.params)
		if got != c.want || ok != c.ok {
			t.Errorf("%v: got %v %v, want %v %v", c.params, got, ok, c.want, c.ok)
		}
	}

	if _, ok := f.Seconds("b", nil); ok {
		t.Error("an unrecorded file has a time")
	}
}

func TestUpdateReplacesWhatWasMeasuredAndDropsDeletedFiles(t *testing.T) {
	f := New()
	f.Files["kept"] = map[string]float64{"phone": 1}
	f.Files["measured"] = map[string]float64{"phone": 1, "desktop": 2}
	f.Files["deleted"] = map[string]float64{"phone": 1}

	f.Update(map[string]map[string]float64{
		"measured": {"phone": 4.44},
		"new":      {"phone": 3},
	}, func(p string) bool { return p != "deleted" })

	want := map[string]map[string]float64{
		"kept":     {"phone": 1},
		"measured": {"phone": 4.4, "desktop": 2},
		"new":      {"phone": 3},
	}
	if !reflect.DeepEqual(f.Files, want) {
		t.Fatalf("got %v, want %v", f.Files, want)
	}
}

func open(t *testing.T, name string) *os.File {
	t.Helper()

	r, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	return r
}

func TestGoTestsKeepsTopLevelTests(t *testing.T) {
	got, err := GoTests(open(t, "gotest.json"))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]float64{
		"example.com/m/api":   {"TestOrders": 3.25, "TestRefunds": 4, "TestSlow": 0},
		"example.com/m/money": {"TestRound": 0.05},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGoFilesAddsTestsUpByFile(t *testing.T) {
	measured, unknown := GoFiles(
		map[string]float64{"TestA": 1, "TestB": 2, "TestGone": 3},
		map[string]string{"TestA": "api/a_test.go", "TestB": "api/a_test.go"},
		Plain,
	)

	if !reflect.DeepEqual(measured, map[string]map[string]float64{"api/a_test.go": {Plain: 3}}) {
		t.Fatalf("measured %v", measured)
	}

	if !reflect.DeepEqual(unknown, []string{"TestGone"}) {
		t.Fatalf("unknown %v", unknown)
	}
}

func TestPlaywrightAddsAttemptsUpPerFileAndProject(t *testing.T) {
	got, err := Playwright(open(t, "playwright.json"), "e2e")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]float64{
		"e2e/checkout.spec.ts":       {"phone": 3, "desktop": 0.9},
		"e2e/console/search.spec.ts": {"desktop": 41.2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
