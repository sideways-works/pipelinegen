//go:build pipelinegen_cover

package main

// A coverage build of the server links go-correlator's cover package: while
// the server keeps running across the browser specs, it hands its counters
// to pipelinegen at each spec's end and starts the next spec from zero.
import _ "github.com/sideways-works/pipelinegen-correlator-go/cover"
