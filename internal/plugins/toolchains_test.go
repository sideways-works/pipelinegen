package plugins

import (
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestToolchainsTold(t *testing.T) {
	for _, c := range []struct {
		needs      []string
		toolchains map[string]string
		want       bool
	}{
		{nil, nil, true},
		{[]string{"go>=go.mod"}, nil, false},
		{[]string{"go>=go.mod"}, map[string]string{"go": "go1.27.1"}, true},
		{[]string{"go>=go.mod", "node>=22"}, map[string]string{"go": "go1.27.1"}, false},
		{[]string{"go>=go.mod"}, map[string]string{"go": ""}, false},
	} {
		if got := ToolchainsTold(plugin.Description{Needs: c.needs, Toolchains: c.toolchains}); got != c.want {
			t.Errorf("needs %v, toolchains %v: %v", c.needs, c.toolchains, got)
		}
	}
}
