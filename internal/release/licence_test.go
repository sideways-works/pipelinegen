package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	agplHead   = "                    GNU AFFERO GENERAL PUBLIC LICENSE\n                       Version 3, 19 November 2007\n"
	apacheHead = "\n                                 Apache License\n                           Version 2.0, January 2004\n"
)

func TestALicenceIsTheNearestLicenseFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "LICENSE"), agplHead)
	writeFile(t, filepath.Join(root, "plugin", "LICENSE"), apacheHead)
	writeFile(t, filepath.Join(root, "odd", "LICENSE"), "MIT License\n")

	for dir, want := range map[string]string{
		root: agplLicence,
		filepath.Join(root, "internal", "report"): agplLicence,
		filepath.Join(root, "plugin"):             apacheLicence,
		filepath.Join(root, "plugin", "sub"):      apacheLicence,
	} {
		if got, err := licenceOf(dir, ""); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", dir, got, err, want)
		}
	}

	if _, err := licenceOf(filepath.Join(root, "odd"), ""); err == nil {
		t.Error("a licence neither the Apache License nor the AGPL was taken")
	}

	bare := t.TempDir()
	writeFile(t, filepath.Join(bare, ".git", "HEAD"), "ref: refs/heads/main\n")

	if _, err := licenceOf(filepath.Join(bare, "x"), ""); err == nil {
		t.Error("a repository without a LICENSE has a licence")
	}
}

func TestTheHeaderSaysWhichLicenceEachOwnPartHas(t *testing.T) {
	n := &noticeText{first: true}
	n.header("go-driver", "release binaries", agplLicence, []ownCode{
		{"github.com/sideways-works/pipelinegen", apacheLicence},
		{"github.com/sideways-works/pipelinegen-plugins", agplLicence},
		{"github.com/sideways-works/plugkit", apacheLicence},
	})

	got := strings.Join(strings.Fields(n.b.String()), " ")
	want := "go-driver " + copyright + " This product is licensed under the GNU Affero General Public License, version 3 or (at your option) any later version (see LICENSE). " +
		"Its release binaries include code from github.com/sideways-works/pipelinegen-plugins, which has the same copyright holder and licence, " +
		"github.com/sideways-works/pipelinegen and github.com/sideways-works/plugkit, which have the same copyright holder and are licensed under the Apache License, Version 2.0, " +
		"and the third-party software below, each under its own licence, reproduced here as those licences require."

	if got != want {
		t.Errorf("header:\n%s\nwant\n%s", got, want)
	}

	n = &noticeText{first: true}
	n.header("pipelinegen", "release binaries", agplLicence, nil)

	if got := strings.Join(strings.Fields(n.b.String()), " "); !strings.Contains(got, "version 3 or (at your option) any later version (see LICENSE). Its release binaries include the third-party software below") {
		t.Errorf("header without own code:\n%s", got)
	}

	for line := range strings.SplitSeq(n.b.String(), "\n") {
		if len(line) > 73 {
			t.Errorf("a line longer than 73: %q", line)
		}
	}
}

func TestAnSDKDirectoryOfTheModuleIsNamed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "LICENSE"), agplHead)
	writeFile(t, filepath.Join(root, "plugin", "LICENSE"), apacheHead)

	o, ok, err := ownDirectory(filepath.Join(root, "plugin", "wire"), "example.com/tool", root, agplLicence)
	if err != nil || !ok || o != (ownCode{"example.com/tool/plugin", apacheLicence}) {
		t.Errorf("plugin: %v %v %v", o, ok, err)
	}

	if _, ok, err := ownDirectory(filepath.Join(root, "internal"), "example.com/tool", root, agplLicence); ok || err != nil {
		t.Errorf("a directory under the release's own licence was named: %v", err)
	}
}

func TestABundledWorkspacePackageHasItsOwnLicence(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "LICENSE"), agplHead)
	writeFile(t, filepath.Join(root, "package-lock.json"), `{"packages": {}}`)
	writeFile(t, filepath.Join(root, "correlator", "react", "package.json"), `{"name": "@sideways-works/react-correlator"}`)
	writeFile(t, filepath.Join(root, "sbom", "ts", "LICENSE"), apacheHead)
	writeFile(t, filepath.Join(root, "sbom", "ts", "package.json"), `{"name": "@sideways-works/ts-sbom"}`)

	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@sideways-works"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join("..", "..", "sbom", "ts"), filepath.Join(root, "node_modules", "@sideways-works", "ts-sbom")); err != nil {
		t.Fatal(err)
	}

	text, err := NodeNotice(filepath.Join(root, "correlator", "react"), "react-correlator", []string{"../../node_modules/@sideways-works/ts-sbom"})
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(got, "version 3 or (at your option) any later version (see LICENSE). Its release files include code from github.com/sideways-works/pipelinegen-plugins, which has the same copyright holder and is licensed under the Apache License, Version 2.0,") {
		t.Errorf("notice:\n%s", text)
	}
}
