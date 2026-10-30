package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

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
