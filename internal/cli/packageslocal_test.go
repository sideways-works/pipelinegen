package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// fakeContainerTool is a docker that logs each call to log and has every image the test names: a
// cached image once a commit made it.
const fakeContainerTool = `#!/bin/sh
echo "$*" >> "$LOG"
for ref; do :; done
mark="$LOG.$(echo "$ref" | tr -c 'a-z0-9' _)"
case "$1 $2" in
"image inspect")
	case "$ref" in
	pipelinegen-cache/*) [ -f "$mark" ] || exit 1; echo sha256:made ;;
	*) case "$4" in *Os*) echo linux/amd64 ;; *Id*) echo "sha256:$(echo "$ref" | cksum | cut -d' ' -f1)" ;; *) echo null ;; esac ;;
	esac ;;
commit*) touch "$mark" ;;
run*) [ -z "$FAIL_INSTALL" ] || exit 1 ;;
esac
exit 0
`

// A batch on this machine gets its packages first, then its memory check, then its setup: the
// packages' images are made before the memory is asked, and a batch refused for memory had them made.
func TestPackagesAreInstalledBeforeTheMemoryCheck(t *testing.T) {
	fakeMemory(t, 3000, time.Hour)

	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("LOG", log)

	tool := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(tool, []byte(fakeContainerTool), 0o700); err != nil {
		t.Fatal(err)
	}

	b := imageBuild(t, false)
	b.box.tool = tool
	b.cfg.Packages = config.Packages{{Name: "jq"}}

	peaks := func(context.Context) (map[string][]int64, error) {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("the test's log: %w", err)
		}

		_, err = f.WriteString("memory\n")

		return map[string][]int64{"a": {6000}}, errors.Join(err, f.Close())
	}

	var err error

	stderrOf(t, func() { err = b.readyWith(t.Context(), peaks, []string{"a"}) })

	if !errors.Is(err, errNoMemoryHere) {
		t.Fatalf("not refused for memory: %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	calls := strings.Split(strings.TrimSpace(string(data)), "\n")

	commits := 0

	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "commit "):
			commits++
		case c == "memory" && i != len(calls)-1:
			t.Fatalf("the memory was asked before the packages were installed: %q", calls)
		}
	}

	// the prepare's image and the batch's are each made once
	if commits != 2 || calls[len(calls)-1] != "memory" {
		t.Fatalf("calls: %q", calls)
	}

	// the tasks then run in the images made, and make nothing again
	for _, kind := range []string{kindPrepare, kindBatch} {
		img, err := b.image(t.Context(), kind, "")
		if err != nil {
			t.Fatal(err)
		}

		p, err := b.packaged(t.Context(), img, kind)
		if err != nil || !strings.HasPrefix(p.ref, packages.LocalRepository+"/") || len(p.before) != 1 {
			t.Fatalf("%s: %+v %v", kind, p, err)
		}
	}

	if again, err := os.ReadFile(log); err != nil || len(again) != len(data) {
		t.Fatalf("made again: %s %v", again, err)
	}
}

// A suite whose packages can't be installed is not run, and says why; the run goes on to the others.
func TestASuiteWhosePackagesFailIsNotRun(t *testing.T) {
	t.Setenv("LOG", filepath.Join(t.TempDir(), "calls"))
	t.Setenv("FAIL_INSTALL", "1")

	tool := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(tool, []byte(fakeContainerTool), 0o700); err != nil {
		t.Fatal(err)
	}

	b := imageBuild(t, false)
	b.box.tool = tool
	b.cfg.Packages = config.Packages{{Name: "jq"}}

	none := func(context.Context) (map[string][]int64, error) { return map[string][]int64{}, nil }

	var err error

	stderrOf(t, func() { err = b.readyWith(t.Context(), none, []string{"a"}) })

	tally, ok := unready("unit", []string{"a"}, err)
	if !errors.Is(err, errImage) || !ok || !strings.Contains(tally.short, "suite unit's packages (jq)") {
		t.Fatalf("%v: %+v", err, tally)
	}

	var out strings.Builder
	if err := localSummary(&out, strings.Repeat("a", 40), []suiteTally{tally}, false); err == nil || !strings.Contains(out.String(), "unit: 1 not run: ") {
		t.Fatalf("%v: %s", err, out.String())
	}
}
