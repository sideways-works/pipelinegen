package plugin

import (
	"path"
	"strings"
)

// Kinds are the kinds of plugin, in the order a repository's name is
// matched against them. A runner's bootstrap, written in shell, keeps the
// same list, and its tests hold it equal to this one.
var Kinds = []string{KindSBOM, KindCorrelator, KindRunner, KindDriver, KindConnector}

// RepositoryPrefix starts the name of a plugin's repository that names its
// kind first.
const RepositoryPrefix = "pipelinegen-"

// Program is the name of the program a repository holds (owner/name or a
// URL), which names its release files and its ./cmd/NAME: a repository
// pipelinegen-KIND-NAME holds NAME-KIND, any other the program of its own
// name.
func Program(repository string) string {
	name := strings.TrimSuffix(path.Base(strings.TrimSuffix(repository, "/")), ".git")

	rest, ok := strings.CutPrefix(name, RepositoryPrefix)
	if !ok {
		return name
	}

	for _, kind := range Kinds {
		if n, ok := strings.CutPrefix(rest, kind+"-"); ok && n != "" {
			return n + "-" + kind
		}
	}

	return name
}
