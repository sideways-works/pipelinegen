package cli

import "testing"

func TestGraphBoundWithoutPluginsWalkedSelectsWhatSelectorsSelect(t *testing.T) {
	graphBoundEquivalence(t, false, 0, graphBoundHalf)
}
