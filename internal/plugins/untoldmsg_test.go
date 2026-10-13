package plugins

import (
	"strings"
	"testing"
)

func TestACantTellWarningCarriesThePluginsWords(t *testing.T) {
	s := newSetup(t, "links")
	s.r.Write("cant-tell/units", "")
	s.r.Commit("can't tell")

	set := s.set(t)

	if _, err := set.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
		t.Fatal(err)
	}

	var got []string

	for _, w := range set.Report().Warnings {
		if strings.Contains(w, "could not tell") {
			got = append(got, w)
		}
	}

	if len(got) == 0 || !strings.Contains(got[0], "cant-tell/units") || strings.Contains(got[0], "\n") {
		t.Fatalf("warnings %q", got)
	}
}
