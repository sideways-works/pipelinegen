package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/cli"
	"github.com/sideways-works/pipelinegen/internal/config"
)

func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func read(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

const (
	configPage   = "docs/reference/config.md"
	commandsPage = "docs/reference/commands.md"
)

var keyRowRE = regexp.MustCompile("^\\|\\s*`([a-z0-9_.<>\\[\\]]+)`\\s*\\|")

// ownedElsewhere are keys whose own keys belong to another part of
// pipelinegen, which documents and validates them.
var ownedElsewhere = []string{"generated[]."}

// aliases are keys that are another key by a second name: their own keys
// are documented once, under the key they stand for.
var aliases = map[string]string{"runs.<name>.trigger.changes.": "runs.<name>.trigger.pull_requests."}

func documentedAs(k string) string {
	for alias, of := range aliases {
		if rest, ok := strings.CutPrefix(k, alias); ok {
			return of + rest
		}
	}

	return k
}

func TestConfigReferenceHasEveryKey(t *testing.T) {
	root := repoRoot(t)
	page := read(t, filepath.Join(root, configPage))

	documented := map[string]bool{}

	for line := range strings.SplitSeq(page, "\n") {
		if m := keyRowRE.FindStringSubmatch(line); m != nil {
			documented[m[1]] = true
		}
	}

	var keys []string
	for _, k := range config.Keys() {
		keys = append(keys, k.Path)
	}

	if len(keys) < 20 {
		t.Fatalf("found only %d config keys: %v", len(keys), keys)
	}

	for _, k := range keys {
		if !documented[documentedAs(k)] {
			t.Errorf("%s has no row for the config key `%s`", configPage, k)
		}
	}

	for k := range documented {
		if slices.Contains(keys, k) || slices.ContainsFunc(ownedElsewhere, func(p string) bool { return strings.HasPrefix(k, p) }) {
			continue
		}

		t.Errorf("%s documents `%s`, which the config doesn't have", configPage, k)
	}
}

var commandHeadingRE = regexp.MustCompile("^`pipelinegen ([a-z -]+)`$")

func TestCommandReferenceHasEveryCommandAndFlag(t *testing.T) {
	root := repoRoot(t)
	page := read(t, filepath.Join(root, commandsPage))

	cmds := cli.Commands()
	if len(cmds) == 0 {
		t.Fatal("no commands")
	}

	sections := map[string]Section{}

	for _, s := range Sections(page, 2) {
		if m := commandHeadingRE.FindStringSubmatch(s.Heading); m != nil {
			sections[m[1]] = s
		}
	}

	registered := map[string]bool{}

	for _, c := range cmds {
		registered[c.Name] = true

		s, ok := sections[c.Name]
		if !ok {
			t.Errorf("%s has no section ## `pipelinegen %s`", commandsPage, c.Name)

			continue
		}

		if !s.Released() {
			t.Errorf("%s: `pipelinegen %s` is on main now: remove %q", commandsPage, c.Name, NotReleased)
		}

		for _, f := range c.Flags {
			if !strings.Contains(s.Body, "`--"+f.Name) {
				t.Errorf("%s: `pipelinegen %s` doesn't document its flag `--%s`", commandsPage, c.Name, f.Name)
			}
		}
	}

	for name, s := range sections {
		// a nested command (report start) is released with its parent
		parent, _, _ := strings.Cut(name, " ")
		if !registered[name] && !registered[parent] && s.Released() {
			t.Errorf("%s documents `pipelinegen %s`, which isn't a command: mark it %q", commandsPage, name, NotReleased)
		}
	}
}

func TestDocsLinksLeadSomewhere(t *testing.T) {
	root := repoRoot(t)

	pages := []string{"README.md"}

	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			pages = append(pages, filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))))
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	n := 0

	for _, p := range pages {
		for _, l := range Links(p, read(t, filepath.Join(root, p))) {
			n++

			if err := Resolve(root, l); err != nil {
				t.Error(err)
			}
		}
	}

	if n == 0 {
		t.Fatal("found no links at all")
	}
}

func TestLinksAndAnchors(t *testing.T) {
	text := "# Title\n\nSee [a](b.md#the-part), [web](https://x.y) and `[c](d)`.\n\n```\n[e](f)\n```\n\n## The part\n## The part\n### `pipelinegen slices`\n"

	got := Links("docs/x.md", text)
	if len(got) != 1 || got[0].Target != "b.md#the-part" || got[0].Line != 3 {
		t.Fatalf("links: %+v", got)
	}

	a := Anchors(text)
	for _, want := range []string{"title", "the-part", "the-part-1", "pipelinegen-slices"} {
		if !a[want] {
			t.Errorf("no anchor %s in %v", want, a)
		}
	}
}

func TestResolve(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("# A\n\n## Here\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for target, ok := range map[string]bool{
		"a.md": true, "a.md#here": true, "#here": true, "a.md#gone": false, "b.md": false, "../..": false, "../docs": true,
	} {
		err := Resolve(root, Link{Page: "docs/a.md", Target: target, Line: 1})
		if (err == nil) != ok {
			t.Errorf("%s: %v", target, err)
		}
	}
}

func TestSectionsAndMarker(t *testing.T) {
	page := "# Commands\n\n## `pipelinegen a`\n\nNot in a release yet.\n\n```\n## not a heading\n```\n\n## `pipelinegen b`\n\nText.\n"

	s := Sections(page, 2)
	if len(s) != 2 || s[0].Heading != "`pipelinegen a`" || s[0].Released() || !s[1].Released() {
		t.Fatalf("sections: %+v", s)
	}
}

var docRefRE = regexp.MustCompile(`docs/[a-z0-9_/-]+\.md(?:#[a-z0-9_-]+)?`)

func TestCodeDocReferencesResolve(t *testing.T) {
	root := repoRoot(t)
	n := 0

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}

			for _, ref := range docRefRE.FindAllString(read(t, p), -1) {
				n++

				if err := Resolve(root, Link{Page: "README.md", Target: ref, Line: 1}); err != nil {
					t.Errorf("%s: %v", strings.TrimPrefix(p, root+string(filepath.Separator)), err)
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if n == 0 {
		t.Fatal("found no references at all")
	}
}
