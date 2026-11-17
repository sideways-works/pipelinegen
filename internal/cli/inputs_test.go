package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestBookInputsAreTheSlicesData(t *testing.T) {
	book := newPlanBook(nil)
	tree := &deps.Tree{Units: map[string]deps.Unit{}}

	book.keep("b", tree, []deps.Slice{
		{Suite: "s", Name: "a_test.go", Width: "executed", Data: []string{"//d:x"}},
		{Suite: "s", Name: "a_test.go", Width: "dependents", Data: []string{"//d:x"}},
	})
	book.keep("h", tree, []deps.Slice{{Suite: "s", Name: "a_test.go", Width: "executed", Data: []string{"//d:x", "//d:y"}}})

	inputs := book.inputsHook()

	if got, err := inputs("b", "s", "a_test.go"); err != nil || !slices.Equal(got, []string{"//d:x"}) {
		t.Fatalf("base: %v %v", got, err)
	}

	if got, err := inputs("h", "s", "a_test.go"); err != nil || !slices.Equal(got, []string{"//d:x", "//d:y"}) {
		t.Fatalf("head: %v %v", got, err)
	}

	for _, c := range [][2]string{{"x", "a_test.go"}, {"h", "b_test.go"}} {
		if _, err := inputs(c[0], "s", c[1]); !errors.Is(err, errNoInputs) || !strings.Contains(err.Error(), c[0]) {
			t.Fatalf("%v: %v", c, err)
		}
	}

	var none *planBook
	if none.inputsHook() != nil {
		t.Fatal("no book, no hook")
	}
}

func TestBookInputsOfWalkedSlices(t *testing.T) {
	book := newPlanBook(nil)
	tree := &deps.Tree{
		Units: map[string]deps.Unit{}, FileUnits: map[string][]string{"api/doc.yaml": {"api:op"}},
		ByUnits: map[string]string{"api/doc.yaml": "document-units"}, Providers: map[string]string{"api:op": "api", "go:f": "go"},
	}

	cfg := &config.Config{Suites: map[string]config.Suite{"s": {
		TextInputs: []config.TextInputs{{Tests: []string{"golden_test.go"}, Paths: []string{"api/*.yaml"}}},
	}}}

	book.keepInputs(cfg, "h", tree, []plugins.BindSlice{
		{Suite: "s", Name: "listed_test.go", HasList: true, List: []string{"api:op"}, Data: []string{"//api:doc.yaml", "//d:x"}},
		{Suite: "s", Name: "other_list_test.go", HasList: true, List: []string{"go:f"}, Data: []string{"//api:doc.yaml", "//d:x"}},
		{Suite: "s", Name: "new_test.go", Data: []string{"//api:doc.yaml", "//d:x"}},
		{Suite: "s", Name: "golden_test.go", HasList: true, Data: []string{"//api:doc.yaml", "//d:x"}},
		{Suite: "u", Name: "unobserved_test.go", HasList: true, List: []string{"go:f"}, Data: []string{"//api:doc.yaml", "//d:x"}},
	})

	inputs := book.inputsHook()

	if got, err := inputs("h", "s", "listed_test.go"); err != nil || !slices.Equal(got, []string{"//d:x"}) {
		t.Fatalf("listed: %v %v", got, err)
	}

	if got, err := inputs("h", "s", "new_test.go"); err != nil || !slices.Equal(got, []string{"//api:doc.yaml", "//d:x"}) {
		t.Fatalf("without a list: %v %v", got, err)
	}

	if got, err := inputs("h", "s", "other_list_test.go"); err != nil || !slices.Equal(got, []string{"//d:x"}) {
		t.Fatalf("another list: %v %v", got, err)
	}

	if got, err := inputs("h", "u", "unobserved_test.go"); err != nil || !slices.Equal(got, []string{"//api:doc.yaml", "//d:x"}) {
		t.Fatalf("unobserved: %v %v", got, err)
	}

	if got, err := inputs("h", "s", "golden_test.go"); err != nil || !slices.Equal(got, []string{"//api:doc.yaml", "//d:x"}) {
		t.Fatalf("a reader of its text: %v %v", got, err)
	}

	if _, err := inputs("h", "s", "other_test.go"); !errors.Is(err, errNoInputs) {
		t.Fatalf("a slice never kept: %v", err)
	}
}

func TestBookInputsWithoutTheOtherTreeKeepGroupedFilesWhole(t *testing.T) {
	book := newPlanBook(nil)
	tree := &deps.Tree{
		Units: map[string]deps.Unit{"tbl:a": {ID: "tbl:a", File: "db/1.sql"}}, FileUnits: map[string][]string{"db/1.sql": {"tbl:a"}},
		ByUnits: map[string]string{"db/1.sql": "by-table"}, Providers: map[string]string{"tbl:a": "tables"},
	}
	groups := &bazel.Groups{Members: map[string][]string{"//db:all": {"//db:1.sql", "//db:x.txt"}}}

	book.keepInputs(&config.Config{}, "h", tree, []plugins.BindSlice{{
		Suite: "s", Name: "a_test.go", HasList: true, List: []string{"tbl:a"},
		Tops: []string{"//db:all"}, Groups: groups, Data: []string{"//db:1.sql", "//db:x.txt"},
	}})

	if got, err := book.inputs("h", "", false, "s", "a_test.go"); err != nil || !slices.Equal(got, []string{"//db:x.txt"}) {
		t.Fatalf("one tree: %v %v", got, err)
	}

	// paired with a tree the book never kept, an added file can't be told from one that was there
	if got, err := book.inputs("h", "b", true, "s", "a_test.go"); err != nil || !slices.Equal(got, []string{"//db:all"}) {
		t.Fatalf("unpaired: %v %v", got, err)
	}
}
