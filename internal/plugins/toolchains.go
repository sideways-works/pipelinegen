package plugins

import (
	"regexp"

	"github.com/sideways-works/pipelinegen/plugin"
)

var needName = regexp.MustCompile(`^[A-Za-z0-9_.-]+`)

// ToolchainsTold tells whether the plugin reported every toolchain it needs: only then may an
// answer that depends on the toolchain be cached.
func ToolchainsTold(d plugin.Description) bool {
	for _, n := range d.Needs {
		name := needName.FindString(n)
		if name == "" || d.Toolchains[name] == "" {
			return false
		}
	}

	return true
}
