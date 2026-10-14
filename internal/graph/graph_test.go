package graph

import "testing"

func TestHitsAreOneSuitesSlicesInOneWidthByName(t *testing.T) {
	s := &Selection{Selected: map[Key][]Path{
		{Suite: "api", Name: "b_test.go", Width: Executed}:   {{Listed: "go:b#func:B", Changed: "go:c#func:C"}},
		{Suite: "api", Name: "a_test.go", Width: Executed}:   nil,
		{Suite: "api", Name: "c_test.go", Width: Dependents}: nil,
		{Suite: "e2e", Name: "x.spec.ts", Width: Executed}:   nil,
		{Suite: "api", Name: "_sample.api", Width: Executed}: nil,
	}}

	got := s.Hits("api", Executed)

	want := []string{"_sample.api", "a_test.go", "b_test.go"}
	if len(got) != len(want) {
		t.Fatalf("hits = %v, want %v", got, want)
	}

	for i, h := range got {
		if h.Slice != want[i] {
			t.Fatalf("hit %d = %s, want %s", i, h.Slice, want[i])
		}
	}

	if len(got[2].Paths) != 1 || got[2].Paths[0].Changed != "go:c#func:C" {
		t.Fatalf("b_test.go's paths = %v", got[2].Paths)
	}
}
