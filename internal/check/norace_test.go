//go:build !race

package check

// raceSlowdown allows for the race detector's cost in timing tests.
const raceSlowdown = 1
