package plugins

import (
	"slices"
	"strings"
	"testing"
)

func TestAMacHoldsTheseAsOneFile(t *testing.T) {
	composed, decomposed := "docs/café.md", "docs/café.md"
	hangul, jamo := "i18n/한.json", "i18n/한.json"

	got := collidingPaths([]string{
		"README.md", "readme.md", composed, decomposed, hangul, jamo,
		"src/a.go", "src/b.go", "src/A_test.go",
	})

	want := [][]string{{"README.md", "readme.md"}, {decomposed, composed}, {hangul, jamo}}
	for _, w := range want {
		slices.Sort(w)
	}

	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}

	for _, w := range want {
		if !slices.ContainsFunc(got, func(g []string) bool { return slices.Equal(g, w) }) {
			t.Errorf("%q not found in %q", w, got)
		}
	}
}

func TestNoPluginIsAskedAboutThem(t *testing.T) {
	s := &Set{}
	files := map[string]string{"README.md": "1", "readme.md": "2", "src/a.go": "3"}

	asked := s.apartOnEveryFilesystem(files)
	if len(asked) != 1 || asked["src/a.go"] != "3" || len(files) != 3 {
		t.Fatalf("asked %v, files %v", asked, files)
	}

	s.apartOnEveryFilesystem(files)

	if w := s.report.Warnings; len(w) != 1 || !strings.HasPrefix(w[0], "README.md and readme.md are one file on a filesystem that folds case") {
		t.Fatalf("warnings %q", w)
	}
}
