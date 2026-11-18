package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// templatesRepo is a plugins repository whose templates/<kind>/ is a Go module with one program,
// tagged at three versions, the newest a candidate; the file VERSION tells them apart.
func templatesRepo(t *testing.T) string {
	t.Helper()

	src, bare := t.TempDir(), filepath.Join(t.TempDir(), "plugins.git")
	git(t, src, "init", "-q", "-b", "main")

	for _, version := range []string{"v0.1.0", "v0.2.0", "v0.3.0-rc.1"} {
		for _, kind := range plugin.Kinds {
			dir := filepath.Join(src, "templates", kind)
			module := "github.com/o/plugins/templates/" + kind

			writeFile(t, filepath.Join(dir, "go.mod"), "module "+module+"\n\ngo 1.27.1\n")
			writeFile(t, filepath.Join(dir, "VERSION"), version+"\n")
			writeFile(t, filepath.Join(dir, "cmd", "example-"+kind, "main.go"),
				"package main\n\nimport \""+module+"/internal/answer\"\n\n// example-"+kind+" answers the protocol.\nfunc main() { answer.Serve(\"example-"+kind+"\") }\n")
			writeFile(t, filepath.Join(dir, "internal", "answer", "answer.go"), "package answer\n\nfunc Serve(string) {}\n")
			writeFile(t, filepath.Join(dir, "tools", "release.sh"), "#!/bin/sh\necho example-"+kind+"\n")

			if err := os.Chmod(filepath.Join(dir, "tools", "release.sh"), 0o755); err != nil { //nolint:gosec // a script
				t.Fatal(err)
			}
		}

		git(t, src, "add", "-A")
		git(t, src, "commit", "-q", "-m", "templates "+version)
		git(t, src, "tag", "templates/"+version)
	}

	git(t, src, "tag", "driver/go/v9.0.0")
	git(t, t.TempDir(), "clone", "-q", "--bare", src, bare)

	return "file://" + bare
}

func TestNewStartsAPluginOfEveryKindFromItsTemplate(t *testing.T) {
	from := templatesRepo(t)

	for _, kind := range plugin.Kinds {
		into := filepath.Join(t.TempDir(), "demo")

		stdout, stderr, code := pinsRun(t, "plugin", "new", kind, "demo", "--from", from, "--into", into)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", kind, code, stderr)
		}

		module := "github.com/o/plugins/" + kind + "/demo"
		if want := "wrote the " + kind + " template of " + from + " templates/v0.2.0 into " + into + ": module " + module + ", program demo-" + kind + ", 5 files\n"; stdout != want {
			t.Errorf("%s: printed %q", kind, stdout)
		}

		for file, want := range map[string]string{
			"go.mod":  "module " + module + "\n\ngo 1.27.1\n",
			"VERSION": "v0.2.0\n",
			"cmd/demo-" + kind + "/main.go": "package main\n\nimport \"" + module + "/internal/answer\"\n\n// demo-" + kind +
				" answers the protocol.\nfunc main() { answer.Serve(\"demo-" + kind + "\") }\n",
			"tools/release.sh": "#!/bin/sh\necho demo-" + kind + "\n",
		} {
			got, err := os.ReadFile(filepath.Join(into, file))
			if err != nil || string(got) != want {
				t.Errorf("%s: %s: %v %q", kind, file, err, got)
			}
		}

		if info, err := os.Stat(filepath.Join(into, "tools", "release.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s: the script is no longer executable: %v", kind, err)
		}
	}
}

func TestNewTakesAnotherReleaseAndModule(t *testing.T) {
	from := templatesRepo(t)
	into := filepath.Join(t.TempDir(), "x")

	_, stderr, code := pinsRun(t, "plugin", "new", "sbom", "rust", "--from", from, "--into", into, "--version", "v0.3.0-rc.1", "--module", "example.com/rust-sbom")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	for file, want := range map[string]string{"VERSION": "v0.3.0-rc.1\n", "go.mod": "module example.com/rust-sbom\n\ngo 1.27.1\n"} {
		if got, err := os.ReadFile(filepath.Join(into, file)); err != nil || string(got) != want {
			t.Errorf("%s: %v %q", file, err, got)
		}
	}
}

func TestNewRefusesWhatItCantStart(t *testing.T) {
	from := templatesRepo(t)
	full := t.TempDir()
	writeFile(t, filepath.Join(full, "README.md"), "mine\n")

	for args, says := range map[string]string{
		"linter go":                        "kind \"linter\": one of sbom, correlator, runner, driver, connector",
		"sbom go-sbom":                     "without the kind",
		"sbom Go":                          "lower case letters",
		"sbom go --into " + full:           "holds files already",
		"sbom go --version v9.9.9":         "couldn't find remote ref refs/tags/templates/v9.9.9",
		"sbom go --version 1.0":            "--version \"1.0\": a version vX.Y.Z",
		"sbom":                             "plugin new <kind> <name>",
		"sbom go --from " + from + ".none": "plugins.git.none",
	} {
		// the case's own flags come last, and so win
		_, stderr, code := pinsRun(t, append([]string{"plugin", "new", "--from", from, "--into", filepath.Join(t.TempDir(), "x")}, strings.Fields(args)...)...)
		if code == 0 || !strings.Contains(stderr, says) {
			t.Errorf("%s: exit %d: %s", args, code, stderr)
		}
	}
}

// A template whose kind the release lacks, or that holds a link, writes nothing.
func TestNewRefusesATemplateItCantTrust(t *testing.T) {
	src, bare := t.TempDir(), filepath.Join(t.TempDir(), "plugins.git")
	git(t, src, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(src, "templates", "sbom", "go.mod"), "module x/templates/sbom\n")
	writeFile(t, filepath.Join(src, "templates", "sbom", "cmd", "example-sbom", "main.go"), "package main\n")

	if err := os.Symlink("/etc/passwd", filepath.Join(src, "templates", "sbom", "cmd", "example-sbom", "passwd")); err != nil {
		t.Fatal(err)
	}

	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "templates")
	git(t, src, "tag", "templates/v1.0.0")
	git(t, t.TempDir(), "clone", "-q", "--bare", src, bare)

	for kind, says := range map[string]string{"sbom": "a link or special file", "driver": "templates/v1.0.0 of file://" + bare + " has no templates/driver"} {
		into := filepath.Join(t.TempDir(), "x")

		_, stderr, code := pinsRun(t, "plugin", "new", kind, "go", "--from", "file://"+bare, "--into", into)
		if code == 0 || !strings.Contains(stderr, says) {
			t.Errorf("%s: exit %d: %s", kind, code, stderr)
		}

		if _, err := os.Stat(into); err == nil {
			t.Errorf("%s: wrote %s", kind, into)
		}
	}
}

// selfNamingTemplate is a plugins repository whose sbom template names its own directory in its
// BUILD.bazel and README.
func selfNamingTemplate(t *testing.T) string {
	t.Helper()

	src, bare := t.TempDir(), filepath.Join(t.TempDir(), "plugins.git")
	git(t, src, "init", "-q", "-b", "main")

	dir := filepath.Join(src, "templates", "sbom")
	writeFile(t, filepath.Join(dir, "go.mod"), "module github.com/o/plugins/templates/sbom\n")
	writeFile(t, filepath.Join(dir, "cmd", "example-sbom", "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "BUILD.bazel"), "template(dir = \"templates/sbom\")\n")
	writeFile(t, filepath.Join(dir, "README.md"), "bazel run //templates/sbom:test\nbazel run //templates/sbomx:other\n")

	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "templates")
	git(t, src, "tag", "templates/v1.0.0")
	git(t, t.TempDir(), "clone", "-q", "--bare", src, bare)

	return "file://" + bare
}

func TestNewNamesItsOwnDirectoryWhereTheTemplateNamedIts(t *testing.T) {
	from := selfNamingTemplate(t)

	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")

	for into, dir := range map[string]string{
		filepath.Join(repo, "plugins", "rust"):  "plugins/rust",
		filepath.Join(repo, "sbom", "rust"):     "sbom/rust",
		filepath.Join(repo, "a", "b", "c", "d"): "a/b/c/d",
	} {
		_, stderr, code := pinsRun(t, "plugin", "new", "sbom", "rust", "--from", from, "--into", into)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", into, code, stderr)
		}

		for file, want := range map[string]string{
			"BUILD.bazel": "template(dir = \"" + dir + "\")\n",
			"README.md":   "bazel run //" + dir + ":test\nbazel run //templates/sbomx:other\n",
			"go.mod":      "module github.com/o/plugins/sbom/rust\n",
		} {
			if got, err := os.ReadFile(filepath.Join(into, file)); err != nil || string(got) != want {
				t.Errorf("%s: %s: %v %q", into, file, err, got)
			}
		}
	}
}

// Outside a git work tree the new plugin is named where the plugins repository keeps one.
func TestNewOutsideAWorkTreeNamesKindAndName(t *testing.T) {
	from := selfNamingTemplate(t)
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", outside)

	into := filepath.Join(outside, "rust")

	_, stderr, code := pinsRun(t, "plugin", "new", "sbom", "rust", "--from", from, "--into", into)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if got, err := os.ReadFile(filepath.Join(into, "BUILD.bazel")); err != nil || string(got) != "template(dir = \"sbom/rust\")\n" {
		t.Fatalf("%v %q", err, got)
	}
}
