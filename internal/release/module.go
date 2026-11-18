package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// target is what a release releases: a repository's root module, or a module in a directory of
// it (a repository of several plugins), with the git tag of the release. A module in a directory
// is tagged DIR/vX.Y.Z and released as such; its program is named by the module itself, not by
// the repository.
type target struct {
	// repo is the repository's top level, absolute.
	repo string
	// sub is the module's directory in the repository, slash-separated; empty for the root.
	sub string
	// tag is the release's git tag: the version for the root, SUB/version for a directory.
	tag string
	// version is the release's version, vX.Y.Z: what the programs report and the build sets.
	version string
}

// place is the repository a directory belongs to and the directory within it: dir itself when it
// is no git checkout, which then refuses as one without the tag.
func place(ctx context.Context, dir string) (string, string, error) {
	dir, err := filepath.Abs(dir)
	if err == nil {
		dir, err = filepath.EvalSymlinks(dir)
	}

	if err != nil {
		return "", "", fmt.Errorf("release: %w", err)
	}

	top, err := output(ctx, dir, "git", "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return dir, "", nil //nolint:nilerr // no checkout: the tag lookup says so
	}

	if top, err = filepath.EvalSymlinks(top); err != nil {
		return dir, "", nil //nolint:nilerr // as above
	}

	rel, err := filepath.Rel(top, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return dir, "", nil //nolint:nilerr // the directory is the repository's root
	}

	return top, filepath.ToSlash(rel), nil
}

// locate is the module dir holds and the release tag names: for a module in a directory, the tag
// is DIR/vX.Y.Z, given whole or as its version alone.
func locate(ctx context.Context, dir, tag string) (target, error) {
	repo, sub, err := place(ctx, dir)
	if err != nil {
		return target{}, err
	}

	m := target{repo: repo, sub: sub, tag: tag, version: tag}

	if sub != "" {
		if v, ok := strings.CutPrefix(tag, sub+"/"); ok {
			m.version = v
		} else {
			m.tag = sub + "/" + tag
		}
	}

	if !versionTag.MatchString(m.version) {
		if sub != "" {
			return m, refuse("%s is no version tag of %s (%s/v1.2.3, or %s/v1.2.3-rc.1 for a candidate)", tag, sub, sub, sub)
		}

		return m, refuse("%s is no version tag (v1.2.3, or v1.2.3-rc.1 for a candidate)", tag)
	}

	return m, nil
}

// dir is the module's directory in a checkout of the repository at root.
func (m target) dir(root string) string { return filepath.Join(root, filepath.FromSlash(m.sub)) }

// path is a file of the module as the repository names it.
func (m target) path(file string) string {
	if m.sub == "" {
		return file
	}

	return m.sub + "/" + file
}

// nestedTag is the tag a module nested in this one at sub is released with.
func (m target) nestedTag(sub string) string { return m.path(sub) + "/" + m.version }

// legalFile is the module's own LICENSE or NOTICE in a checkout at root, else the repository's.
func (m target) legalFile(root, name string) string {
	if own := filepath.Join(m.dir(root), name); exists(own) {
		return own
	}

	return filepath.Join(root, name)
}

// program is the name of the program a module releases. The root module of a repository named
// pipelinegen-KIND-NAME releases NAME-KIND, any other the program of the repository's name. A
// module in a directory names it itself: a Node package by its package name without its scope, a
// Go module by its one program under cmd/, or among several the one its module path names
// (.../KIND/NAME holds NAME-KIND), or by its module path when its root package is the program.
func (m target) program(ctx context.Context, mdir string) (string, error) {
	if m.sub == "" {
		return plugin.Program(repositoryName(ctx, m.repo)), nil
	}

	if !exists(filepath.Join(mdir, "go.mod")) && exists(filepath.Join(mdir, "package.json")) {
		return packageName(mdir)
	}

	mod := moduleLine(filepath.Join(mdir, "go.mod"))

	var mains []string

	dirs, _ := os.ReadDir(filepath.Join(mdir, "cmd")) //nolint:errcheck // no cmd: no program there
	for _, d := range dirs {
		dir := filepath.Join(mdir, "cmd", d.Name())
		if d.IsDir() && !exists(filepath.Join(dir, "go.mod")) && rootMain(dir) {
			mains = append(mains, d.Name())
		}
	}

	switch {
	case len(mains) == 1:
		return mains[0], nil
	case len(mains) == 0:
		return plugin.Program(path.Base(mod)), nil
	}

	if kind, name := path.Base(path.Dir(mod)), path.Base(mod); slices.Contains(plugin.Kinds, kind) && slices.Contains(mains, name+"-"+kind) {
		return name + "-" + kind, nil
	}

	return "", refuse("%s holds several programs under cmd/ (%s) and its module path names none of them", m.sub, strings.Join(mains, ", "))
}

func packageName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json")) //nolint:gosec // the package's own file
	if err != nil {
		return "", fmt.Errorf("release: %w", err)
	}

	var pkg struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Name == "" {
		return "", refuse("%s/package.json names no package", dir)
	}

	return path.Base(pkg.Name), nil
}
