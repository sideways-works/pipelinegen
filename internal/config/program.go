package config

import (
	"path"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/pin"
)

// Program is the program the plugin's release files are named for; see ProgramOf.
func (p Plugin) Program() string { return ProgramOf(p.Repository, p.Path) }

// ProgramOf is the program a repository holds at path: go-driver for driver/go, and with no path
// go-sbom for both owner/go-sbom and owner/pipelinegen-sbom-go.
func ProgramOf(repository, dir string) string {
	if dir != "" {
		return pin.Pin{Path: dir}.ProgramName()
	}

	return plugin.Program(repository)
}

// CloneURL is the URL git clones the repository from: owner/name is GitHub's.
func CloneURL(repository string) string {
	if !strings.Contains(repository, ":") && strings.Count(repository, "/") == 1 {
		return "https://github.com/" + repository
	}

	return repository
}

// programRepository is owner/PROGRAM, so both spellings of one program's repository compare
// equal.
func programRepository(repository string) string {
	r := strings.TrimSuffix(strings.TrimSuffix(repository, "/"), ".git")
	if dir := path.Dir(r); dir != "." {
		return dir + "/" + plugin.Program(r)
	}

	return plugin.Program(r)
}
