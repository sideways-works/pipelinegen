package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestListsMatterOnlyToRecordedSuites(t *testing.T) {
	cfg := &config.Config{Suites: map[string]config.Suite{"unit": {Tests: []string{"*_test.go"}, Bind: config.BindGraph}}}
	if hasRecorded(cfg) {
		t.Fatal("a graph suite reads lists")
	}

	cfg.Suites["e2e"] = config.Suite{Tests: []string{"*.spec.ts"}, Bind: config.BindRecorded}
	if !hasRecorded(cfg) {
		t.Fatal("a recorded suite reads none")
	}
}

func TestNoListsAreWorthAWordWithoutARecordedSuite(t *testing.T) {
	var b strings.Builder

	graph := &config.Config{Suites: map[string]config.Suite{"unit": {Tests: []string{"*_test.go"}, Bind: config.BindGraph}}}
	warnf(listsWarnings(graph, &b), "no remote")

	recorded := &config.Config{Suites: map[string]config.Suite{"e2e": {Tests: []string{"*.spec.ts"}, Bind: config.BindRecorded}}}
	warnf(listsWarnings(recorded, &b), "no remote")

	if b.String() != "no remote" {
		t.Fatalf("got %q", b.String())
	}
}
