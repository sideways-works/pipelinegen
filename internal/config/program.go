package config

import (
	"path"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Program is the program the plugin's repository holds: go-sbom for both owner/go-sbom and
// owner/pipelinegen-sbom-go.
func (p Plugin) Program() string { return plugin.Program(p.Repository) }

// programRepository is owner/PROGRAM, so both spellings of one program's repository compare
// equal.
func programRepository(repository string) string {
	r := strings.TrimSuffix(strings.TrimSuffix(repository, "/"), ".git")
	if dir := path.Dir(r); dir != "." {
		return dir + "/" + plugin.Program(r)
	}

	return plugin.Program(r)
}
