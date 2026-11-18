package release

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTheToolsNoticeIsWhatItsReleaseLinks(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile(filepath.Join(root, "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := GoNotice(t.Context(), root, "pipelinegen", []string{"./cmd/pipelinegen"})
	if err != nil {
		t.Fatal(err)
	}

	if got != string(want) {
		t.Errorf("the NOTICE the release writes differs from the repository's:\n%s", got)
	}
}

func TestFilesAreListedAsThePublishedReleasesList(t *testing.T) {
	want := []string{
		"go-driver-darwin-amd64", "go-driver-darwin-arm64", "go-driver-linux-amd64", "go-driver-linux-arm64",
		"LICENSE", "NOTICE", "playwright.mjs", "plugin.json", "react-correlator.mjs", "shell-driver-linux-amd64",
	}

	got := slices.Clone(want)
	slices.Reverse(got)
	slices.SortFunc(got, collation)

	if !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestFold(t *testing.T) {
	line := "This product is licensed under the Apache License, Version 2.0 (see LICENSE). Its release binaries include code from github.com/sideways-works/pipelinegen, which has the same copyright holder and licence."
	want := []string{
		"This product is licensed under the Apache License, Version 2.0 (see",
		"LICENSE). Its release binaries include code from",
		"github.com/sideways-works/pipelinegen, which has the same copyright",
		"holder and licence.",
	}

	if got := fold(line); !slices.Equal(got, want) {
		t.Errorf("fold:\n%s", strings.Join(got, "\n"))
	}

	if got := fold(strings.Repeat("x", 80)); len(got) != 2 || len(got[0]) != 73 {
		t.Errorf("a word longer than the width: %q", got)
	}
}

func TestHeaderNamesTheFamilysOwnCode(t *testing.T) {
	n := &noticeText{first: true}
	n.header("shell-driver", "release binaries", []string{"github.com/sideways-works/pipelinegen", "github.com/sideways-works/pipelinegen-sbom-go"})

	want := "shell-driver\n" + copyright + "\n\n" +
		"This product is licensed under the Apache License, Version 2.0 (see\n" +
		"LICENSE). Its release binaries include code from\n" +
		"github.com/sideways-works/pipelinegen and\n" +
		"github.com/sideways-works/pipelinegen-sbom-go, which have the same\n" +
		"copyright holder and licence, and the third-party software below, each\n" +
		"under its own licence, reproduced here as those licences require.\n\n"
	if got := n.b.String(); got != want {
		t.Errorf("header:\n%s\nwant\n%s", got, want)
	}

	if got := familyRepository("go-sbom"); got != "pipelinegen-sbom-go" {
		t.Errorf("go-sbom lives in %s", got)
	}

	if got := familyRepository("pipelinegen"); got != "pipelinegen" {
		t.Errorf("pipelinegen lives in %s", got)
	}
}

func TestLeadingComment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.h")
	text := "/*\n * Copyright (c) someone\n *\n * Permission is granted.\n */\n#include <x.h>\n"

	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	n := &noticeText{first: true}
	if err := n.leadingComment(p); err != nil {
		t.Fatal(err)
	}

	if got := n.b.String(); got != "Copyright (c) someone\n\nPermission is granted.\n" {
		t.Errorf("comment %q", got)
	}

	if err := os.WriteFile(p, []byte("#include <x.h>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&noticeText{}).leadingComment(p); err == nil {
		t.Error("a file without a leading comment")
	}
}

func TestSums(t *testing.T) {
	out := t.TempDir()

	for name, text := range map[string]string{"b": "2", "A": "1", ".hidden": "x"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := writeSums(out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(out, sumsFile))
	if err != nil {
		t.Fatal(err)
	}

	want := "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b  A\n" +
		"d4735e3a265e16eee03f59718b9b5d03019c07d8b6c51f90da3a666eec13ab35  b\n"
	if string(data) != want {
		t.Errorf("SHA256SUMS:\n%s", data)
	}

	if err := checkSums(out); err != nil {
		t.Errorf("check: %v", err)
	}

	if err := os.WriteFile(filepath.Join(out, "b"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkSums(out); err == nil {
		t.Error("a changed file passed")
	}
}

func TestARepositoryWithoutAnOriginIsNamedByItsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pipelinegen-driver-gadget")

	for name, text := range map[string]string{
		"go.mod":                    "module example.com/gadget\n\ngo 1.27\n",
		"cmd/gadget-driver/main.go": "package main\n\nfunc main() {}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(dir)

	text, err := Notice(t.Context(), IO{Stdout: io.Discard, Stderr: io.Discard}, ".")
	if err != nil || !strings.HasPrefix(text, "gadget-driver\n") {
		t.Errorf("notice: %v\n%s", err, text)
	}
}

func TestAWorkspacesPackageTakesItsVersionsFromTheWorkspacesLockfile(t *testing.T) {
	root := t.TempDir()

	for name, content := range map[string]string{
		"package-lock.json":             `{"packages": {"node_modules/left-pad": {"version": "1.3.0"}}}`,
		"node_modules/left-pad/LICENSE": "left-pad's licence\n",
		"tools/widget/package.json":     `{"name": "@owner/widget"}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	text, err := NodeNotice(filepath.Join(root, "tools", "widget"), "widget", []string{"../../node_modules/left-pad"})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(text, "left-pad 1.3.0\n") || !strings.Contains(text, "left-pad's licence") {
		t.Errorf("notice:\n%s", text)
	}
}

func TestAWorkspacesPackageIsInstalledWhereTheWorkspaceIs(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "tools", "widget")

	for _, d := range []string{pkg, filepath.Join(root, "node_modules")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if installed(pkg) {
		t.Error("installed without a lockfile above that says where the workspace is")
	}

	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !installed(pkg) {
		t.Error("not installed, though the workspace's node_modules is there")
	}
}
