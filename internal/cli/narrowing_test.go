package cli

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestNarrowingsFollowTheHighestLevel(t *testing.T) {
	if got := narrowingLevel(&config.Config{Narrowing: 1, Suites: map[string]config.Suite{"a": {}, "b": {Narrowing: 4}}}); got != 4 {
		t.Errorf("a suite's higher level: %d", got)
	}

	if got := narrowingLevel(&config.Config{Narrowing: 2, Suites: map[string]config.Suite{"a": {}}}); got != 2 {
		t.Errorf("the config's level: %d", got)
	}
}

func TestEveryNarrowingCanBeSwitchedOff(t *testing.T) {
	for _, n := range plugins.CoreNarrowings {
		if n.OffAt > config.MaxNarrowing {
			t.Errorf("%s is off only from %d, above the highest level %d", n.Name, n.OffAt, config.MaxNarrowing)
		}
	}
}
