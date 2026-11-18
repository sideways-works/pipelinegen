//go:build !pipelinegen_forward

// Package forwardtag says whether the tool was built with the pipelinegen_forward tag, which binds recorded
// suites forward. It stands alone because a file the build leaves out stops declaration-by-declaration reading.
package forwardtag

const Tagged = false
