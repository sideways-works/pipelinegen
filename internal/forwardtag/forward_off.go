//go:build !pipelinegen_forward

// Package forwardtag says whether the tool was built with the
// pipelinegen_forward tag, which makes planning bind recorded suites
// forward. It is a package of its own because files the build leaves out
// keep a package from being followed declaration by declaration.
package forwardtag

// Tagged is set in a build with the pipelinegen_forward tag.
const Tagged = false
