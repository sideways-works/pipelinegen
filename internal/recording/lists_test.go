package recording

import (
	"slices"
	"testing"
)

func TestMerge(t *testing.T) {
	lists := Lists{"s/a": {"p#func:A", "p#func:Old"}, "s/b": {"p#func:B"}}
	rec := Lists{"s/a": {"p#func:New"}}

	grown := Merge(lists, rec, false)
	if !slices.Equal(grown["s/a"], []ID{"p#func:A", "p#func:New", "p#func:Old"}) {
		t.Errorf("a partial run: %v, want the union", grown["s/a"])
	}

	full := Merge(lists, rec, true)
	if !slices.Equal(full["s/a"], []ID{"p#func:New"}) {
		t.Errorf("a full run: %v, want only what the slice executed now", full["s/a"])
	}

	if !slices.Equal(full["s/b"], []ID{"p#func:B"}) {
		t.Errorf("a slice the full run didn't record lost its list: %v", full["s/b"])
	}

	if !slices.Equal(lists["s/a"], []ID{"p#func:A", "p#func:Old"}) {
		t.Error("Merge changed its input")
	}
}

func TestUnion(t *testing.T) {
	got := Union(Lists{"s/a": {"p#func:B"}}, Lists{"s/a": {"p#func:A", "p#func:B"}, "s/c": {"p#func:C"}})
	if !slices.Equal(got["s/a"], []ID{"p#func:A", "p#func:B"}) || len(got["s/c"]) != 1 {
		t.Errorf("Union = %v", got)
	}
}
