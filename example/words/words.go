package main

import "strings"

// Upper returns s in capitals.
func Upper(s string) string {
	return strings.ToUpper(s)
}

// Count returns how many words s holds.
func Count(s string) int {
	return len(strings.Fields(s))
}
