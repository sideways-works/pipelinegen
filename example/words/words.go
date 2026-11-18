package main

import "strings"

func Upper(s string) string {
	return strings.ToUpper(s)
}

func Count(s string) int {
	return len(strings.Fields(s))
}
