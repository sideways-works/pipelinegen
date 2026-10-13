package plugins

import (
	"os"
	"path/filepath"
	"strings"
)

// MissingNeeds are the needs whose program isn't on env's PATH. A need
// names a program and may add a version ("go>=1.27", "node>=22"), which
// the plugin or driver checks itself.
func MissingNeeds(needs []string, env []string) []string {
	path := ""

	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}

	var missing []string

	for _, n := range needs {
		prog := n
		if i := strings.IndexAny(n, "<>=~ "); i >= 0 {
			prog = n[:i]
		}

		if prog == "" || onPath(prog, path) {
			continue
		}

		missing = append(missing, n)
	}

	return missing
}

func onPath(prog, path string) bool {
	for _, dir := range filepath.SplitList(path) {
		if info, err := os.Stat(filepath.Join(dir, prog)); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}

	return false
}
