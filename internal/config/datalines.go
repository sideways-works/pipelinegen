package config

import (
	"bufio"
	"io"
	"regexp"
)

var dataComment = regexp.MustCompile(`pipelinegen:data\s+(\S+)`)

// DataLines is how many lines from its top a test file's data lines may sit in.
const DataLines = 30

// DataComments are the deps a test file's pipelinegen:data lines name, in its first DataLines
// lines: Bazel labels and unit ids alike.
func DataComments(r io.Reader) []string {
	var out []string

	sc := bufio.NewScanner(r)
	for n := 0; n < DataLines && sc.Scan(); n++ {
		for _, m := range dataComment.FindAllStringSubmatch(sc.Text(), -1) {
			out = append(out, m[1])
		}
	}

	return out
}
