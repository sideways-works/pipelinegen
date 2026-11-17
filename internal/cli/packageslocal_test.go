package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/container"
	"github.com/sideways-works/pipelinegen/internal/packages"
)

// Steps that run on this machine install nothing: the packages it lacks are named, once a build.
func TestTheMachineIsCheckedForTheSuitesPackages(t *testing.T) {
	b := imageBuild(t, false)
	b.cfg.Packages = config.Packages{{Name: "sh"}, {Name: "pipelinegen-no-such-package"}}

	out := stderrOf(t, func() {
		b.hostPackages(t.Context())
		b.hostPackages(t.Context())
	})

	if strings.Count(out, "suite unit declares packages this machine has under no command or installed package of their name: pipelinegen-no-such-package;") != 1 {
		t.Fatalf("stderr: %q", out)
	}

	b = imageBuild(t, false)
	b.cfg.Packages = config.Packages{{Name: "sh"}}

	if out := stderrOf(t, func() { b.hostPackages(t.Context()) }); out != "" {
		t.Fatalf("all there: %q", out)
	}
}

// A suite without packages runs in its image as it is, with nothing before its steps.
func TestNoPackagesNoInstall(t *testing.T) {
	b := imageBuild(t, false)
	img := taskImage{ref: "busybox:1.37.0"}

	before, err := b.withPackages(t.Context(), &img)
	if err != nil || before != nil || img.ref != "busybox:1.37.0" {
		t.Fatalf("%q %+v %v", before, img, err)
	}
}

// The steps run in the image with the packages installed, made once in the local image store and
// reused; the install before them finds everything there.
func TestTheStepsRunInTheirCachedImage(t *testing.T) {
	tool, err := container.Find("")
	if err != nil || !container.Reachable(t.Context(), tool) {
		t.Skip("neither docker nor podman answers")
	}

	const base = "alpine:3.20"

	b := imageBuild(t, false)
	b.box.tool = tool
	b.cfg.Packages = config.Packages{{Name: "jq"}}

	if _, err := container.Ensure(t.Context(), tool, base, &strings.Builder{}); err != nil {
		t.Skipf("no %s: %v", base, err)
	}

	var said []string

	for range 2 {
		img := taskImage{ref: base}

		out := stderrOf(t, func() {
			before, err := b.withPackages(t.Context(), &img)
			if err != nil || len(before) != 1 || before[0] != packages.Script(b.cfg.PackagesFor("unit")) {
				t.Errorf("%q %v", before, err)
			}
		})

		if !strings.HasPrefix(img.ref, packages.LocalRepository+"/") {
			t.Fatalf("image %s", img.ref)
		}

		t.Cleanup(func() { _ = exec.Command(tool, "rmi", img.ref).Run() }) //nolint:errcheck,noctx // the image this test made

		said = append(said, out)
	}

	if !strings.Contains(said[1], "made before on alpine:3.20") {
		t.Fatalf("not reused: %q", said)
	}
}
