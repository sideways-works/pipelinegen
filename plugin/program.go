package plugin

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// Kinds are in the order a repository's name is matched against them; a runner's shell
// bootstrap keeps the same list, and its tests hold it equal to this one.
var Kinds = []string{KindSBOM, KindCorrelator, KindRunner, KindDriver, KindForge}

var ErrKind = errors.New("not a pipelinegen plugin kind")

// CheckKind refuses a kind that isn't one of Kinds: plugins of other systems name kinds of their own,
// some of them alike, and pipelinegen never runs one. connector, this kind's old name, gets the new.
func CheckKind(kind string) error {
	switch {
	case slices.Contains(Kinds, kind):
		return nil
	case kind == "connector":
		return fmt.Errorf("%w: kind connector is not a pipelinegen plugin kind; pipelinegen's is %s", ErrKind, KindForge)
	}

	return fmt.Errorf("%w: kind %q is not a pipelinegen plugin kind; pipelinegen's are %s", ErrKind, kind, strings.Join(Kinds, ", "))
}

// RepositoryPrefix starts the name of a plugin's repository that names its kind first.
const RepositoryPrefix = "pipelinegen-"

// Program is the program a repository (owner/name or a URL) holds, naming its release files and
// ./cmd/NAME: pipelinegen-KIND-NAME holds NAME-KIND, any other repository its own name.
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
