package releasetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// The pipeline's release job: the version tag on the head, only after the nightly passed on it.
func TestTaggedReleasesTheHeadsVersionTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	for name, tc := range map[string]struct {
		nightly, tag, says, published string
		released, ok                  bool
	}{
		"the nightly passed": {nightly: "success", tag: "v1.2.3", released: true, ok: true},
		// released by hand already: done when the release holds this build, whatever the nightly
		"released already as built": {nightly: "failure", tag: "v1.2.3", published: "same", ok: true, says: "released already"},
		"released already, other files": {nightly: "success", tag: "v1.2.3", published: "other", says: "SHA256SUMS"},
		"the nightly failed": {nightly: "failure", tag: "v1.2.3", says: "no run of every test passed"},
		"a candidate's tag":  {nightly: "success", tag: "v1.2.4-rc.1", says: "no version tag on the head"},
		"no tag on the head": {nightly: "success", says: "no version tag on the head"},
	} {
		t.Run(name, func(t *testing.T) {
			work := widget(t, nil)
			repo := filepath.Join(work, "widget")

			run(t, repo, "git", "tag", "-d", "v1.2.3")

			if tc.tag != "" {
				run(t, repo, "git", "tag", tc.tag)
			}

			bin, calls := filepath.Join(work, "bin"), filepath.Join(work, "calls")
			view := "exit 1"
			if tc.published != "" {
				view = "exit 0"
			}

			// a published release is the build's files, or with "other" one of them changed
			download := `d=$(eval echo \${$#}); cp out/* "$d"`
			if tc.published == "other" {
				download += `; printf x >> "$d/LICENSE"`
			}

			gh := `#!/bin/sh
echo "$*" >> '` + calls + `'
case "$1 $2" in
"api repos/owner/widget/commits/"*) echo "pipelinegen/nightly ` + tc.nightly + `" ;;
"release view") ` + view + ` ;;
"release download") ` + download + ` ;;
esac
`

			if err := os.MkdirAll(bin, 0o750); err != nil {
				t.Fatal(err)
			}

			if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "gh"), []byte(gh)); err != nil {
				t.Fatal(err)
			}

			env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			cmd := exec.CommandContext(t.Context(), releaseBin(t), "tagged", "widget", "owner/widget", "out", "pipelinegen/nightly")
			cmd.Dir, cmd.Env = work, env

			out, err := cmd.CombinedOutput()
			got, _ := os.ReadFile(calls) //nolint:errcheck // no calls is an empty file

			created := strings.Contains(string(got), "release create v1.2.3 -R owner/widget")
			if (err == nil) != tc.ok || created != tc.released || !strings.Contains(string(out), tc.says) {
				t.Errorf("exit %v, gh called with\n%s\n%s", err, got, out)
			}

			if _, err := os.Stat(filepath.Join(work, "out")); !tc.released && tc.published == "" && err == nil {
				t.Error("built although it isn't released")
			}
		})
	}
}
