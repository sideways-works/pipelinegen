package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// platforms are the systems a Go program is released for; the macOS builds
// are cross-built here and never run in CI.
var platforms = []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"}

var reserved = []string{pluginFile, sumsFile, licenseFile, noticeFileName}

const (
	licenseFile    = "LICENSE"
	noticeFileName = "NOTICE"
	pluginFile     = "plugin.json"
)

// releaseList is the file a Go module names its extra release files in,
// one path per line; # starts a comment.
const releaseList = ".pipelinegen-release"

// sourceOf is a fresh clone of dir at the tag in dest, so the build sees
// exactly the tagged commit and go build can stamp it.
func sourceOf(ctx context.Context, o IO, dir, tag, dest string) error {
	if !hasTag(ctx, dir, tag) {
		return refuse("%s has no tag %s", dir, tag)
	}

	if err := o.run(ctx, "", os.Environ(), "git", "clone", "-q", "--no-checkout", dir, dest); err != nil {
		return err
	}

	if err := o.run(ctx, "", os.Environ(), "git", "-C", dest, "checkout", "-q", "--detach", tag); err != nil {
		return err
	}

	return nestedTags(ctx, dest, tag)
}

func moduleLine(goMod string) string {
	data, err := os.ReadFile(goMod) //nolint:gosec // a module's own go.mod
	if err != nil {
		return ""
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "module"); ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) {
			return strings.TrimSpace(rest)
		}
	}

	return ""
}

// nestedTags: a Go module nested under the root module's path (SUB/go.mod declaring ROOT/SUB) is released
// with the repository, so its tag SUB/TAG must name the same commit; other nested modules are not.
func nestedTags(ctx context.Context, src, tag string) error {
	subs, err := releasedWith(src)
	if err != nil || len(subs) == 0 {
		return err
	}

	commit, err := output(ctx, src, "git", "-C", src, "rev-parse", tag+"^{commit}")
	if err != nil {
		return err
	}

	root := moduleLine(filepath.Join(src, "go.mod"))

	for _, sub := range subs {
		got, err := output(ctx, src, "git", "-C", src, "rev-parse", "-q", "--verify", "refs/tags/"+sub+"/"+tag+"^{commit}")
		if err != nil || got != commit {
			return refuse("the nested module %s/%s needs the tag %s/%s on the commit of %s", root, sub, sub, tag, tag)
		}
	}

	return nil
}

// releasedWith are the directories, slash-separated from src, of the nested modules released with
// src's root module: those whose path is the root's and their directory.
func releasedWith(src string) ([]string, error) {
	if !exists(filepath.Join(src, "go.mod")) {
		return nil, nil
	}

	root := moduleLine(filepath.Join(src, "go.mod"))

	var subs []string

	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}

		if d.IsDir() || d.Name() != "go.mod" || filepath.Dir(p) == src {
			return nil
		}

		rel, _ := filepath.Rel(src, filepath.Dir(p)) //nolint:errcheck // under src
		if sub := filepath.ToSlash(rel); moduleLine(p) == root+"/"+sub {
			subs = append(subs, sub)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("nested modules: %w", err)
	}

	slices.Sort(subs)

	return subs, nil
}

// versionVar is a package-level `var Version = "dev"`, which the build
// sets to the tag.
var versionVar = regexp.MustCompile(`(?m)^var Version = "dev"`)

func buildGo(ctx context.Context, o IO, src, name, tag, out, main string) error {
	module, err := output(ctx, src, "go", "list", "-m")
	if err != nil {
		return err
	}

	var ldflags strings.Builder

	// no symbol table and no build id, so the two builds of a tag give the same bytes
	ldflags.WriteString("-s -w -buildid=")

	var files []string

	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			data, err := os.ReadFile(p) //nolint:gosec // the module's own source
			if err == nil && versionVar.Match(data) {
				files = append(files, p)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	slices.Sort(files)

	for _, f := range files {
		rel, _ := filepath.Rel(src, filepath.Dir(f)) //nolint:errcheck // under src

		pkg := filepath.ToSlash(rel)

		switch pkg {
		case main:
			pkg = "main"
		case ".":
			pkg = module
		default:
			pkg = module + "/" + pkg
		}

		ldflags.WriteString(" -X " + pkg + ".Version=" + tag)
	}

	for _, platform := range platforms {
		goos, goarch, _ := strings.Cut(platform, "-")
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)

		if err := o.run(ctx, src, env, "go", "build", "-trimpath", "-ldflags="+ldflags.String(), "-o", filepath.Join(out, name+"-"+platform), "./"+main); err != nil {
			return err
		}
	}

	for _, platform := range platforms {
		if info, err := os.Stat(filepath.Join(out, name+"-"+platform)); err != nil || info.Size() == 0 {
			return refuse("the build wrote no %s", filepath.Join(out, name+"-"+platform))
		}
	}

	return nil
}

// program is one program a Go repository releases, built from its main package main (cmd/NAME,
// or . for a main package at the root).
type program struct{ name, main string }

// goPrograms are the programs a Go module releases: name's, from cmd/NAME or a main package at the
// root, and beside cmd/NAME every other main package under cmd/, each by its directory's name.
func goPrograms(src, name string) []program {
	if !exists(filepath.Join(src, "cmd", name)) {
		if rootMain(src) {
			return []program{{name: plugin.Program(path.Base(moduleLine(filepath.Join(src, "go.mod")))), main: "."}}
		}

		return nil
	}

	out := []program{{name: name, main: "cmd/" + name}}

	dirs, _ := os.ReadDir(filepath.Join(src, "cmd")) //nolint:errcheck // cmd exists; an unreadable one adds no program

	for _, d := range dirs {
		// a directory with a go.mod of its own is another module, which the root's go build can't build
		dir := filepath.Join(src, "cmd", d.Name())
		if d.IsDir() && d.Name() != name && !exists(filepath.Join(dir, "go.mod")) && rootMain(dir) {
			out = append(out, program{name: d.Name(), main: "cmd/" + d.Name()})
		}
	}

	return out
}

// describeFile is where a program's describe answer goes: plugin.json for the repository's own
// program, <name>.plugin.json for each other one that is a plugin, none for one that isn't.
func describeFile(p program, first bool) string {
	switch {
	case first && p.name != "pipelinegen":
		return pluginFile
	case first:
		return ""
	}

	for _, kind := range plugin.Kinds {
		if strings.HasSuffix(p.name, "-"+kind) {
			return p.name + ".plugin.json"
		}
	}

	return ""
}

func rootMain(src string) bool {
	files, _ := filepath.Glob(filepath.Join(src, "*.go")) //nolint:errcheck // a valid pattern

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}

		data, err := os.ReadFile(f) //nolint:gosec // the module's own source
		if err == nil && regexp.MustCompile(`(?m)^package main$`).Match(data) {
			return true
		}
	}

	return false
}

func buildNode(ctx context.Context, o IO, src, name, tag, out string) error {
	quietOut := IO{Stdout: discard{}, Stderr: o.Stderr}

	if err := quietOut.run(ctx, src, os.Environ(), "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		return err
	}

	if err := o.run(ctx, src, os.Environ(), "npm", "pkg", "set", "version="+strings.TrimPrefix(tag, "v")); err != nil {
		return err
	}

	if err := o.run(ctx, src, os.Environ(), "npm", "run", "--silent", "build"); err != nil {
		return err
	}

	built := filepath.Join(src, "dist", name+".mjs")
	if !exists(built) {
		return refuse("the build wrote no dist/%s.mjs", name)
	}

	if err := copyFile(built, filepath.Join(out, name+".mjs")); err != nil {
		return err
	}

	extras, err := output(ctx, src, "node", "-e", `const r = require("./package.json").pipelinegen?.release ?? []; if (!Array.isArray(r) || r.some((p) => typeof p !== "string")) process.exit(2); console.log(r.join("\n"))`)
	if err != nil {
		return refuse("package.json's pipelinegen.release is not a list of paths")
	}

	return releaseExtras(ctx, src, out, []string{name + ".mjs"}, strings.Split(extras, "\n"))
}

func goExtras(ctx context.Context, src, out string, own []string) error {
	data, err := os.ReadFile(filepath.Join(src, releaseList)) //nolint:gosec // the module's own list
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	var paths []string

	for line := range strings.SplitSeq(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}

	return releaseExtras(ctx, src, out, own, paths)
}

// releaseExtras copies the files a repository names for its release into out by base name; each must be
// committed, and the build must leave it as committed.
func releaseExtras(ctx context.Context, src, out string, own, paths []string) error {
	for _, extra := range paths {
		if extra == "" {
			continue
		}

		base := path.Base(extra)
		if slices.Contains(own, base) || slices.Contains(reserved, base) {
			return refuse("%s would replace the release's own %s", extra, base)
		}

		if exists(filepath.Join(out, base)) {
			return refuse("two release files are named %s", base)
		}

		if !quiet(ctx, src, "git", "-C", src, "cat-file", "-e", "HEAD:"+extra) {
			return refuse("%s is named for the release but not committed", extra)
		}

		built, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(extra))) //nolint:gosec // a file the repository names
		if err != nil || !sameAsCommitted(ctx, src, extra, built) {
			return refuse("%s differs from the committed file after the build: build it and commit it", extra)
		}

		if err := copyFile(filepath.Join(src, filepath.FromSlash(extra)), filepath.Join(out, base)); err != nil {
			return err
		}
	}

	return nil
}

func sameAsCommitted(ctx context.Context, src, file string, data []byte) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", src, "show", "HEAD:"+file) //nolint:gosec // a path the repository names

	blob, err := cmd.Output()

	return err == nil && bytes.Equal(blob, data)
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from) //nolint:gosec // a release's own file
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	info, err := os.Stat(from)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil { //nolint:gosec // a release's own file
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// describe writes a plugin's describe answer without toolchains as file (plugin.json for the
// repository's own program): one machine's versions say nothing of the release and would differ
// between machines. Being a plugin is never read from answering.
func describe(ctx context.Context, name, exe, out, file string) error {
	if file == "" {
		return nil
	}

	argv := []string{exe, "describe"}
	if strings.HasSuffix(exe, ".mjs") {
		argv = append([]string{"node"}, argv...)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the release's own program
	cmd.Stdin = strings.NewReader(`{"protocol": 1}` + "\n")

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	status := 0

	if err := cmd.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			status = exit.ExitCode()
		} else {
			status = 127
		}
	}

	if status != 0 || !strings.Contains(stdout.String(), `"kind"`) {
		said := stderr.Bytes()
		if len(said) > 2000 {
			said = said[:2000]
		}

		return refuse("the plugin %s didn't answer describe (exit %d): %s", name, status, strings.TrimRight(string(said), "\n"))
	}

	var answer map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &answer); err != nil {
		return refuse("describe's answer isn't a JSON object")
	}

	delete(answer, "toolchains")

	var text bytes.Buffer
	if err := json.NewEncoder(&text).Encode(answer); err != nil {
		return refuse("describe's answer isn't a JSON object")
	}

	if err := os.WriteFile(filepath.Join(out, file), text.Bytes(), 0o644); err != nil { //nolint:gosec // a release file
		return fmt.Errorf("describe: %w", err)
	}

	return nil
}

func repositoryName(ctx context.Context, dir string) string {
	name := dir
	if url, err := output(ctx, dir, "git", "-C", dir, "remote", "get-url", "origin"); err == nil && url != "" {
		name = url
	}

	name = strings.TrimRight(name, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}

	return strings.TrimSuffix(name, ".git")
}

func buildOnce(ctx context.Context, o IO, dir, tag, out string) error {
	work, err := os.MkdirTemp("", "release-")
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	defer func() { _ = os.RemoveAll(work) }() //nolint:errcheck // a scratch directory

	src := filepath.Join(work, "src")
	if err := sourceOf(ctx, o, dir, tag, src); err != nil {
		return err
	}

	name := plugin.Program(repositoryName(ctx, dir))

	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	arch, err := output(ctx, src, "go", "env", "GOARCH")
	if err != nil {
		return err
	}

	isGo := exists(filepath.Join(src, "go.mod"))

	var programs []program
	if isGo {
		programs = goPrograms(src, name)
	}

	switch {
	case programs != nil:
		if err := buildPrograms(ctx, o, src, tag, out, arch, programs); err != nil {
			return err
		}
	case exists(filepath.Join(src, "package.json")):
		if err := buildNode(ctx, o, src, name, tag, out); err != nil {
			return err
		}

		if err := describe(ctx, name, filepath.Join(out, name+".mjs"), out, describeFile(program{name: name}, true)); err != nil {
			return err
		}

		programs = []program{{name: name}}
	default:
		return refuse("%s is neither a Go module with cmd/%s or a main package at its root, nor a Node package", dir, name)
	}

	return finish(ctx, o, dir, src, programs, tag, out, arch)
}

// buildPrograms builds each program for every platform with its describe answer, then the files
// the module names for the release, none of which may take a program's name.
func buildPrograms(ctx context.Context, o IO, src, tag, out, arch string, programs []program) error {
	var own []string

	for i, p := range programs {
		if err := buildGo(ctx, o, src, p.name, tag, out, p.main); err != nil {
			return err
		}

		file := describeFile(p, i == 0)
		if err := describe(ctx, p.name, filepath.Join(out, p.name+"-linux-"+arch), out, file); err != nil {
			return err
		}

		for _, platform := range platforms {
			own = append(own, p.name+"-"+platform)
		}

		if file != "" && i > 0 {
			own = append(own, file)
		}
	}

	return goExtras(ctx, src, out, own)
}

func finish(ctx context.Context, o IO, dir, src string, programs []program, tag, out, arch string) error {
	// the licence, and the notices the programs' third-party code
	// requires, travel with the files they cover
	for _, f := range []string{licenseFile, noticeFileName} {
		if err := copyFile(filepath.Join(src, f), filepath.Join(out, f)); err != nil {
			return err
		}
	}

	if err := checkNotice(ctx, o, src, plugin.Program(repositoryName(ctx, dir))); err != nil {
		return err
	}

	for i, p := range programs {
		if err := checkProgram(ctx, p, i == 0, tag, out, arch); err != nil {
			return err
		}
	}

	return writeSums(out)
}

const versionWait = time.Minute

func checkProgram(ctx context.Context, p program, first bool, tag, out, arch string) error {
	// the repository's own program's plugin.json is checked whenever there is one
	file := describeFile(p, first)
	if first {
		file = pluginFile
	}

	if file != "" {
		pj, err := os.ReadFile(filepath.Join(out, file)) //nolint:gosec // the build's own file

		switch {
		case describeFile(p, first) != "" && err != nil:
			return refuse("the plugin %s has no %s", p.name, file)
		case err == nil && !regexp.MustCompile(`"version": *"`+regexp.QuoteMeta(tag)+`"`).Match(pj):
			return refuse("the built plugin %s doesn't report %s as its version", p.name, tag)
		}
	}

	// a program with a version command must print the tag
	self := filepath.Join(out, p.name+"-linux-"+arch)
	if info, err := os.Stat(self); err == nil && info.Mode()&0o111 != 0 {
		// a program that takes version for something else, a server say, mustn't hold the build
		vctx, cancel := context.WithTimeout(ctx, versionWait)
		defer cancel()

		cmd := exec.CommandContext(vctx, self, "version") //nolint:gosec // the release's own program

		if said, err := cmd.Output(); err == nil {
			if s := strings.TrimRight(string(said), "\n"); s != "" && s != tag {
				return refuse("%s version says %s, not %s", p.name, s, tag)
			}
		}
	}

	return nil
}
