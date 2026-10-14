package graph

import (
	"reflect"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

func tree(units map[string]string, links ...plugin.Link) *deps.Tree {
	t := &deps.Tree{Units: map[string]deps.Unit{}, Providers: map[string]string{}, Links: links}

	for id, d := range units {
		t.Units[id] = deps.Unit{ID: id, Digest: d}
	}

	return t
}

func changedOf(t *testing.T, b, h *deps.Tree, o Options) []Change {
	t.Helper()

	got, err := Changed(b, h, nil, Executed, o)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestChangedIsWhatDiffersByDigestOrPresenceAndNothingElse(t *testing.T) {
	b := tree(map[string]string{"go:a#func:A": "1", "go:a#func:B": "1", "go:a#func:Gone": "1", "go:a#type:T": "1"})
	h := tree(map[string]string{"go:a#func:A": "2", "go:a#func:B": "1", "go:a#func:New": "1", "go:a#type:T": "1"})

	got := changedOf(t, b, h, Options{})
	want := []Change{
		{ID: "go:a#func:A", How: HowDigest},
		{ID: "go:a#func:Gone", How: HowRemoved},
		{ID: "go:a#func:New", How: HowAdded},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

func TestTwoEqualTreesChangeNothing(t *testing.T) {
	units := map[string]string{"go:a#func:A": "1", "go:a#type:T": "1"}
	ln := plugin.Link{From: "go:a#func:A", To: "go:a#type:T", Kind: KindFollow}

	b, h := tree(units), tree(units)
	b.Typed, h.Typed = []plugin.Link{ln}, []plugin.Link{ln}

	if got := changedOf(t, b, h, Options{}); len(got) != 0 {
		t.Fatalf("changed = %v, want nothing", got)
	}
}

func TestALinkInOneTreeOnlyChangesItsFromEnd(t *testing.T) {
	units := map[string]string{"go:a#func:A": "1"}
	b, h := tree(units), tree(units)
	h.Typed = []plugin.Link{{From: "group:a", To: "group:b", Kind: KindFollow}}

	want := []Change{{ID: "group:a", How: HowLinks}}
	if got := changedOf(t, b, h, Options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}

	// the same link in both trees, as a plain link in one and typed in
	// the other, is a different link
	b.Links = []plugin.Link{{From: "group:a", To: "group:b", Kind: plugin.LinkUses}}
	if got := changedOf(t, b, h, Options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

func TestAChangedSBOMPinChangesEveryUnitItListsAndACorrelatorsStandsApart(t *testing.T) {
	units := map[string]string{"go:a#func:A": "1", "ts:x.ts#fn:f": "1"}
	b, h := tree(units), tree(units)

	for _, tr := range []*deps.Tree{b, h} {
		tr.Providers["go:a#func:A"] = "go"
		tr.Providers["ts:x.ts#fn:f"] = "ts"
	}

	b.Units["plugin:go"] = deps.Unit{ID: "plugin:go", Digest: "v1"}
	h.Units["plugin:go"] = deps.Unit{ID: "plugin:go", Digest: "v2"}
	b.Units["plugin:go-uses"] = deps.Unit{ID: "plugin:go-uses", Digest: "v1"}
	h.Units["plugin:go-uses"] = deps.Unit{ID: "plugin:go-uses", Digest: "absent"}
	b.Units["plugin:ts"] = deps.Unit{ID: "plugin:ts", Digest: "v1"}
	h.Units["plugin:ts"] = deps.Unit{ID: "plugin:ts", Digest: "v1"}

	o := Options{Plugins: []Plugin{{Name: "go", SBOM: true}, {Name: "ts", SBOM: true}, {Name: "go-uses"}}}

	want := []Change{
		{ID: "go:a#func:A", How: HowPin},
		{ID: "plugin:go-uses", How: HowPin},
	}

	if got := changedOf(t, b, h, o); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

func TestAnUntoldFileIsItsOwnKindOfChangeAndUnboundIDsAreNoUnits(t *testing.T) {
	b := tree(map[string]string{"file:db/schema.sql": "blob1", "unbound:executed:a": "x"})
	h := tree(map[string]string{"file:db/schema.sql": "blob2", "unbound:executed:a": "y"})
	b.Untold = map[string][]string{"pg": {"db/schema.sql"}}

	want := []Change{{ID: "file:db/schema.sql", How: HowUntold}}
	if got := changedOf(t, b, h, Options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}
