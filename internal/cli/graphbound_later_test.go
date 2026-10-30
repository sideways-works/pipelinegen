package cli

import "testing"

// The second half of the changes sits in a file of its own, so each half runs within a slice's time.
func TestGraphBoundWalkedSelectsWhatSelectorsSelectLater(t *testing.T) {
	graphBoundEquivalence(t, true, graphBoundHalf, len(graphBoundChanges()))
}
