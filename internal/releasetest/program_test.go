package releasetest

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAKindFirstRepositoryReleasesItsProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{"cmd/widget/main.go": "", "cmd/widget-sbom/main.go": program})
	run(t, filepath.Join(work, "widget"), "git", "remote", "set-url", "origin", "https://github.com/owner/pipelinegen-sbom-widget.git")
	renotice(t, work)
	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out")

	sums, err := os.ReadFile(filepath.Join(work, "out", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"widget-sbom-linux-amd64", "widget-sbom-linux-arm64", "widget-sbom-darwin-amd64", "widget-sbom-darwin-arm64", "plugin.json"} {
		if !strings.Contains(string(sums), "  "+f+"\n") {
			t.Errorf("SHA256SUMS lists no %s:\n%s", f, sums)
		}
	}
}

var nodePlugin = map[string]string{
	"package.json": `{"name": "widget-correlator", "version": "0.0.0", "private": true, "scripts": {"build": "node build.mjs"}, "pipelinegen": {"notice": []}}
`,
	"package-lock.json": `{"name": "widget-correlator", "version": "0.0.0", "lockfileVersion": 3, "requires": true, "packages": {"": {"name": "widget-correlator", "version": "0.0.0"}}}
`,
	"build.mjs": `import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
const { version } = JSON.parse(readFileSync("package.json", "utf8"));
mkdirSync("dist", { recursive: true });
writeFileSync("dist/widget-correlator.mjs", "#!/usr/bin/env node\n" +
  "if (process.argv[2] === \"describe\") console.log(JSON.stringify({protocol: 1, kind: \"correlator\", name: \"widget-correlator\", version: \"v" + version + "\"}));\n");
`,
}

func TestAKindFirstNodeRepositoryReleasesItsProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a package twice")
	}

	for _, tool := range []string{"node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("the release runs on Linux and macOS")
	}

	files := map[string]string{"go.mod": "", "cmd/widget/main.go": ""}
	maps.Copy(files, nodePlugin)

	work := widget(t, files)
	run(t, filepath.Join(work, "widget"), "git", "remote", "set-url", "origin", "git@github.com:owner/pipelinegen-correlator-widget.git")
	renotice(t, work)

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
	cmd.Dir = work

	cmd.Env = append(os.Environ(), "npm_config_cache="+t.TempDir(), "npm_config_offline=true")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	sums, err := os.ReadFile(filepath.Join(work, "out", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"widget-correlator.mjs", "plugin.json", "LICENSE", "NOTICE"} {
		if !strings.Contains(string(sums), "  "+f+"\n") {
			t.Errorf("SHA256SUMS lists no %s:\n%s", f, sums)
		}
	}
}

func TestANodePackageNamesTheBundlesItsNoticeCovers(t *testing.T) {
	files := map[string]string{"go.mod": "", "cmd/widget/main.go": ""}
	maps.Copy(files, nodePlugin)
	files["package.json"] = `{"name": "widget-correlator", "version": "0.0.0", "private": true, "scripts": {"build": "node build.mjs"}}
`

	work := widget(t, files)

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "notice", "widget")
	cmd.Dir = work

	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), `package.json names no builds under "pipelinegen": {"notice": [...]}`) {
		t.Errorf("a package without its notice builds: %v\n%s", err, b)
	}
}
