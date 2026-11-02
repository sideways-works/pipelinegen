package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
)

func TestWhyPrintsTheChainHopByHop(t *testing.T) {
	r := affected.Reason{Kind: affected.KindDepends, What: "go:api#func:validate", Hops: []affected.Hop{
		{To: "api:createNote", Kind: "listed", Plugin: "openapi", Label: "POST /notes", File: "api/notes.yaml"},
		{From: "api:createNote", To: "go:api/notesapi#method:ServerInterfaceWrapper.CreateNote", Kind: "uses", Plugin: "api-uses"},
		{From: "go:api/notesapi#method:ServerInterfaceWrapper.CreateNote", To: "go:api#func:validate", Kind: "uses", Plugin: "go-correlator"},
	}}

	want := strings.Join([]string{
		"it requested POST /notes during its recording",
		"    → which uses ServerInterfaceWrapper.CreateNote (a link from api-uses)",
		"    → which uses api.validate (a link from go-correlator), which changed in this commit",
	}, "\n")

	if got := chainText(r, []string{"api-uses", "react"}, false); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}

	if got := chainText(r, nil, true); !strings.Contains(got, "api.validate [go:api#func:validate]") {
		t.Fatalf("with ids:\n%s", got)
	}

	one := affected.Reason{Kind: affected.KindExecuted, What: "go:api#func:validate", Hops: []affected.Hop{{To: "go:api#func:validate", Kind: "listed", Plugin: "go-sbom", File: "api/api.go"}}}
	if got := chainText(one, []string{"go-correlator"}, false); got != "it executed api.validate during its recording (seen by go-correlator; api/api.go), which changed in this commit" {
		t.Fatalf("one hop: %s", got)
	}

	ts := affected.Reason{Kind: affected.KindExecuted, What: "ts:apps/web/src/entities/casefile/api.ts#fn:getCaseFile", Hops: []affected.Hop{{
		To: "ts:apps/web/src/entities/casefile/api.ts#fn:getCaseFile", Kind: "listed", Plugin: "ts",
		Label: "getCaseFile in entities/casefile/api.ts", File: "apps/web/src/entities/casefile/api.ts",
	}}}
	if got := chainText(ts, nil, false); got != "it executed getCaseFile in entities/casefile/api.ts during its recording, which changed in this commit" {
		t.Fatalf("a label naming its file: %s", got)
	}

	gohop := affected.Reason{Kind: affected.KindExecuted, What: "go:x", Hops: []affected.Hop{{
		To: "go:x", Kind: "listed", Plugin: "go-sbom", Label: "casefile.Handler.GetCaseFile", File: "core/userinterface/api/v1/server/casefile/handler.go",
	}}}
	if got := chainText(gohop, nil, false); got != "it executed casefile.Handler.GetCaseFile during its recording (core/userinterface/api/v1/server/casefile/handler.go), which changed in this commit" {
		t.Fatalf("a label without its file: %s", got)
	}

	if got := chainText(affected.Reason{Kind: affected.KindDepends, What: "x"}, nil, false); got != "" {
		t.Fatalf("made up a chain: %s", got)
	}
}

func TestPlainNames(t *testing.T) {
	for id, want := range map[string]string{
		"go:api#func:validate":                                     "api.validate",
		"go:internal/notes#func:Create":                            "notes.Create",
		"go:api/notesapi#method:ServerInterfaceWrapper.CreateNote": "ServerInterfaceWrapper.CreateNote",
		"ts:apps/web/src/api.ts#fn:getCaseFile":                    "getCaseFile",
		"ts:apps/web/src/api.ts#init":                              "the module apps/web/src/api.ts",
		"api:createNote":                                           "the operation createNote",
	} {
		if got := plainName(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

func TestASuiteWhoseSlicesEachRanShowsTheirReasons(t *testing.T) {
	wide := affected.Reason{Kind: affected.KindEverything, What: "it reaches ts:a#init, which can't be told"}
	sp := whySuite{Suite: "browser", All: true, Reason: affected.ReachesEverySlice, Reasons: map[string][]affected.Reason{"x": {wide}}}

	got := ranBecause(&whyPlan{}, sp, "a.spec.ts", "x")
	if len(got) != 1 || !strings.Contains(got[0].Text, "ts:a#init, which can't be told") {
		t.Fatalf("got %+v", got)
	}
}
