package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestCheckCommand(t *testing.T) {
	r := testrepo.New(t)

	out, _, code := run(t, "check", "--root", r.Dir)
	if code != 1 || !strings.Contains(out, ": no slice depends on this file") || !strings.Contains(out, "\n  fix: ") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	out, _, code = run(t, "check", "--root", r.Dir, "--staged")
	if code != 0 || out != "" {
		t.Fatalf("staged, nothing touched: exit %d:\n%s", code, out)
	}

	r.Write("docs/new.md", "new\n")
	r.Git("add", "docs/new.md")

	out, _, code = run(t, "check", "--root", r.Dir, "--staged")
	if code != 1 || !strings.HasPrefix(out, "docs/new.md: no slice depends on this file") {
		t.Fatalf("staged, a new file: exit %d:\n%s", code, out)
	}
}

func TestCheckSummaryCountsByDirectory(t *testing.T) {
	r := testrepo.New(t)
	r.Write("docs/a.md", "a\n")
	r.Write("docs/b.md", "b\n")
	r.Write("stray.txt", "s\n")
	r.Commit("uncovered")

	full, _, _ := run(t, "check", "--root", r.Dir)

	n := 0

	for line := range strings.SplitSeq(strings.TrimSuffix(full, "\n"), "\n") {
		if !strings.HasPrefix(line, " ") {
			n++
		}
	}

	out, _, code := run(t, "check", "--root", r.Dir, "--summary")
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	if !strings.Contains(out, "3\tuncovered-file\tdocs/\n") || !regexp.MustCompile(`(?m)^\d+\tuncovered-file\t\.$`).MatchString(out) ||
		!strings.HasSuffix(out, "\n"+strconv.Itoa(n)+" findings\n") || strings.Contains(out, "fix:") {
		t.Fatalf("summary:\n%s\nfull:\n%s", out, full)
	}
}

// A driver the plugins directory lacks is fetched by its pin, as run fetches it, and asked.
func TestCheckFetchesADriverItLacks(t *testing.T) {
	f, stub, repo := fetchingDriver(t, "release: {version: v1, sha256: "+sumOf(nil)+"}")

	stub = append(stub, []byte("# "+t.Name()+"\n")...)
	serveRelease(repo, "v1", "stub-driver-"+config.Platform(), stub)

	cfgPath := filepath.Join(f.root, config.FileName)
	cfg := strings.NewReplacer(sumOf(nil), sumOf(stub), "with: {log: ", "with: {bogus: 1, log: ").Replace(readFile(t, cfgPath))
	writeFile(t, cfgPath, cfg)

	stdout, stderr, _ := runIn(t, f.root, f.env, "check", "--root", f.root)
	if !strings.Contains(stdout, "setting bogus is not one stub takes") || strings.Contains(stdout, "couldn't ask the driver stub") {
		t.Fatalf("the check didn't ask the fetched driver:\n%s\n%s", stdout, stderr)
	}

	if _, err := os.Stat(filepath.Join(f.env["XDG_CACHE_HOME"], "pipelinegen", "releases", sumOf(stub), "stub-driver")); err != nil {
		t.Fatalf("the release isn't in the cache: %v", err)
	}
}

// A driver pinned by commit has no release to fetch: the check says how to build it.
func TestCheckSaysHowToBuildADriverPinnedByCommit(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)
	f.env["XDG_CACHE_HOME"] = t.TempDir()

	if err := os.Remove(filepath.Join(f.root, "tools", "plugins", "stub")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, _ := runIn(t, f.root, f.env, "check", "--root", f.root)
	if !strings.Contains(stdout, "couldn't ask the driver stub what it is (pinned by commit") ||
		!strings.Contains(stdout, "git clone https://github.com/example/stub stub") || !strings.Contains(stdout, "fix: build it as the reason says") {
		t.Fatalf("no build hint:\n%s\n%s", stdout, stderr)
	}
}
