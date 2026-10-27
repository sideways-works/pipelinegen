package kit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// versionFiles are the files version managers pick a toolchain by, from the
// nearest directory upward.
var versionFiles = []string{".tool-versions", ".go-version", ".node-version", ".nvmrc", ".python-version"}

const copiesCheck = "answers about the kit's copies of the fixture can be cached"

// pinToolchains puts the version files that hold for the fixture at the top
// of the kit's work directory: the kit's copies live away from the fixture,
// and without them a version manager would pick the machine's default
// toolchain there.
func pinToolchains(tree, work string) error {
	if tree == "" || work == "" {
		return nil
	}

	for _, name := range versionFiles {
		src := nearestUp(tree, name)
		if src == "" {
			continue
		}

		if _, err := os.Stat(filepath.Join(work, name)); err == nil {
			continue
		}

		data, err := os.ReadFile(src) //nolint:gosec // a version file above the fixture the caller named
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}

		if err := os.MkdirAll(work, 0o750); err != nil {
			return fmt.Errorf("%s: %w", work, err)
		}

		if err := os.WriteFile(filepath.Join(work, name), data, 0o600); err != nil { //nolint:gosec // name is one of versionFiles
			return fmt.Errorf("%s: %w", work, err)
		}
	}

	return nil
}

// PinToolchains puts the version files that hold for a repository (its own,
// or the nearest above it) at the top of a directory a copy of it is checked
// out under, so a version manager picks the repository's toolchain there
// too. Each call takes them afresh.
func PinToolchains(repo, under string) error {
	for _, name := range versionFiles {
		if err := os.Remove(filepath.Join(under, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", under, err)
		}
	}

	return pinToolchains(repo, under)
}

func nearestUp(dir, name string) string {
	for {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ""
		}

		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}

		dir = up
	}
}

type copies struct {
	uncacheable int
	warnings    []string
}

func (c *copies) note(r plugins.Report) {
	for _, call := range r.Calls {
		if call.Plugin == kitPlugin || call.Plugin == kitSBOM {
			c.uncacheable += call.Uncacheable
		}
	}

	for _, w := range r.Warnings {
		if !slices.Contains(c.warnings, w) {
			c.warnings = append(c.warnings, w)
		}
	}
}

func (k *kit) hint() string {
	if k.copies.uncacheable == 0 && len(k.copies.warnings) == 0 {
		return ""
	}

	return "; the plugin's answers about the kit's copies of the fixture couldn't all be cached, see \"" + copiesCheck + "\""
}

func (k *kit) checkCopies() {
	if k.copies.uncacheable == 0 && len(k.copies.warnings) == 0 {
		k.check(copiesCheck, "")

		return
	}

	var b strings.Builder

	fmt.Fprintf(&b, "%d answers about the copies (under %s) were marked not cacheable", k.copies.uncacheable, k.o.Work)

	if len(k.copies.warnings) > 0 {
		fmt.Fprintf(&b, ", and planning over them warned: %s", strings.Join(firstFew(k.copies.warnings), "; "))
	}

	fmt.Fprintf(&b, ". The plugin's describe reports the toolchains %v. A toolchain picked by directory may differ in the copies: the kit puts the nearest %s at or above the fixture at the top of its work directory, so name the version the fixture needs in one of them", k.desc.Toolchains, strings.Join(versionFiles, ", "))

	k.out = append(k.out, Result{Check: copiesCheck, Problem: b.String(), Note: true})
}
