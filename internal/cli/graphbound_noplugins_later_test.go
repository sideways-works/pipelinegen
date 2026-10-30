package cli

import "testing"

func TestGraphBoundWithoutPluginsWalkedSelectsWhatSelectorsSelectLater(t *testing.T) {
	graphBoundEquivalence(t, false, graphBoundHalf, len(graphBoundChanges()))
}
