package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// OldGit puts a git first on PATH that answers `cat-file --batch-check` and
// `cat-file --batch`, with lazy fetching off, as git before 2.45 does: asked
// about an object a partial clone lacks, it dies with "fatal: could not
// fetch <id> from promisor remote" instead of answering "<id> missing".
func OldGit(t *testing.T) {
	t.Helper()

	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	wrapper := `#!/bin/sh
case " $* " in
*" cat-file --batch-check "*|*" cat-file --batch "*) ;;
*) exec '` + git + `' "$@" ;;
esac
[ -n "${GIT_NO_LAZY_FETCH:-}" ] || exec '` + git + `' "$@"
in=$(cat)
missing=$(printf '%s\n' "$in" | '` + git + `' "$@" | sed -n 's/^\([0-9a-f]*\) missing$/\1/p' | head -n 1)
if [ -n "$missing" ]; then
	echo "warning: lazy fetching disabled; some objects may not be available" >&2
	echo "fatal: could not fetch $missing from promisor remote" >&2
	exit 128
fi
printf '%s\n' "$in" | exec '` + git + `' "$@"
`
	if err := WriteExecutable(t.Context(), filepath.Join(dir, "git"), []byte(wrapper)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
