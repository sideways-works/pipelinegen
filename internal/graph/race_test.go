//go:build race

package graph_test

// raceOn skips time budgets under the race detector, which slows everything
// several times over.
const raceOn = true
