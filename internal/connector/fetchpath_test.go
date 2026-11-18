package connector

import (
	"os"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/forge"
	"github.com/sideways-works/pipelinegen/internal/forge/forgetest"
)

// A connector of the plugins' repository is fetched from its own release, tagged with its path,
// and named for the program its path gives.
func TestAConnectorAtAPathIsFetchedByItsPathsTag(t *testing.T) {
	g := forgetest.Start(t)
	bin := []byte("#!/bin/sh\n# github-connector v0.1.0\n")
	g.Release("sideways-works/pipelinegen-plugins", "connector/github/v0.1.0", false, map[string][]byte{"github-connector-" + config.Platform(): bin})
	g.Release("sideways-works/pipelinegen-plugins", "v0.1.0", false, map[string][]byte{"github-connector-" + config.Platform(): []byte("the repository's own")})

	pin := config.ConnectorPin{
		Name: config.ConnectorGitHub, Repository: "https://github.com/sideways-works/pipelinegen-plugins", Path: "connector/github", Version: "v0.1.0",
		SHA256: config.Checksums{config.Platform(): forgetest.Sum(bin)},
	}

	got, err := Fetch(t.Context(), forge.Client(), "", t.TempDir(), pin)
	if err != nil {
		t.Fatal(err)
	}

	if data, err := os.ReadFile(got); err != nil || string(data) != string(bin) {
		t.Fatalf("fetched %s: %v", got, err)
	}

	if asked := g.Asked(); len(asked) != 1 || asked[0] != "/sideways-works/pipelinegen-plugins/releases/download/connector/github/v0.1.0/github-connector-"+config.Platform() {
		t.Fatalf("asked %v", asked)
	}
}
