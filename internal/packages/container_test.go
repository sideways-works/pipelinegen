package packages

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/container"
)

// tool is docker or podman, when it answers; the tests that need it skip
// without.
func tool(t *testing.T) string {
	t.Helper()

	p, err := container.Find("")
	if err != nil || !container.Reachable(t.Context(), p) {
		t.Skip("neither docker nor podman answers")
	}

	return p
}

// managerImages are small images, one per manager the script knows.
var managerImages = []struct{ manager, image string }{
	{"apk", "alpine:3.20"},
	{"apt-get", "debian:bookworm-slim"},
	{"dnf", "fedora:41"},
	{"microdnf", "registry.access.redhat.com/ubi9/ubi-minimal:9.5"},
	{"yum", "amazonlinux:2"},
}

// TestScriptInImages installs jq with each manager, twice: the second run
// finds it and installs nothing. Each image is removed again unless it was
// here before.
func TestScriptInImages(t *testing.T) {
	tl := tool(t)
	script := Script(config.Packages{{Name: "jq"}})

	for _, mi := range managerImages {
		t.Run(mi.manager, func(t *testing.T) {
			had := container.HasImage(t.Context(), tl, mi.image)
			if !had {
				t.Cleanup(func() { _ = exec.Command(tl, "rmi", mi.image).Run() }) //nolint:errcheck,noctx // the image this test pulled
			}

			cache := t.TempDir()
			// the cache is root's: the container that wrote it empties it
			t.Cleanup(func() {
				_ = exec.Command(tl, "run", "--rm", "-v", cache+":/cache", mi.image, "sh", "-c", "rm -rf /cache/*").Run() //nolint:errcheck,noctx // the test's own files
			})
			out := runIn(t, tl, mi.image, cache, "", script+script+"jq --version\n")

			if strings.Count(out, "pipelinegen: installing jq with "+mi.manager) != 1 || !strings.Contains(out, "jq-") {
				t.Fatalf("installed not once with %s, or no jq:\n%s", mi.manager, out)
			}

			if n := filesUnder(t, cache); n == 0 {
				t.Errorf("%s kept nothing in the download cache", mi.manager)
			}
		})
	}
}

func TestScriptRefusesNotRoot(t *testing.T) {
	tl := tool(t)

	out := runIn(t, tl, "alpine:3.20", "", "--user=65534", "("+Script(config.Packages{{Name: "jq"}})+") || echo failed\n")
	if !strings.Contains(out, "can't install jq with apk: the step runs as user 65534, not root") || !strings.Contains(out, "failed") {
		t.Fatal(out)
	}
}

func runIn(t *testing.T, tl, image, cache, user, script string) string {
	t.Helper()

	args := []string{"run", "--rm"}
	if user != "" {
		args = append(args, user)
	}

	if cache != "" {
		args = append(args, "-v", cache+":/cache", "-e", EnvCache+"=/cache")
	}

	var out bytes.Buffer

	cmd := exec.CommandContext(t.Context(), tl, append(args, image, "sh", "-ec", script)...)
	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", image, err, out.String())
	}

	return out.String()
}

func filesUnder(t *testing.T, dir string) int {
	t.Helper()

	n := 0

	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error { //nolint:errcheck // counts what it can read
		if err == nil && !d.IsDir() {
			n++
		}

		return nil
	})

	return n
}

// TestLocal installs jq on alpine once and reuses the image after.
func TestLocal(t *testing.T) {
	tl := tool(t)

	const base = "alpine:3.20"

	if _, err := container.Ensure(t.Context(), tl, base, os.Stderr); err != nil {
		t.Skipf("no %s: %v", base, err)
	}

	ps := config.Packages{{Name: "jq"}}

	id, err := container.ImageID(t.Context(), tl, base)
	if err != nil {
		t.Fatal(err)
	}

	want := LocalRepository + "/" + Key(id, ps)
	_ = exec.Command(tl, "rmi", want).Run() //nolint:errcheck,noctx // a leftover of an earlier run, if any

	var log bytes.Buffer

	ref, built, err := Local(t.Context(), tl, base, ps, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	t.Cleanup(func() { _ = exec.Command(tl, "rmi", ref).Run() }) //nolint:errcheck,noctx // the image this test made

	if ref != want || !built {
		t.Fatalf("ref %s, built %v; want %s built", ref, built, want)
	}

	again, built, err := Local(t.Context(), tl, base, ps, &log)
	if err != nil || again != ref || built {
		t.Fatalf("again: %s %v %v, want %s reused", again, built, err, ref)
	}

	out := runIn(t, tl, ref, "", "", Script(ps)+"jq --version\n")
	if strings.Contains(out, "installing") || !strings.Contains(out, "jq-") {
		t.Fatalf("the cached image lacks jq:\n%s", out)
	}
}
