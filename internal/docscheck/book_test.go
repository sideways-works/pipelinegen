package docscheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/cli"
	"github.com/sideways-works/pipelinegen/internal/config"
)

const (
	summaryPage    = "docs/SUMMARY.md"
	transcriptsDir = "docs/transcripts"
	// outputToCome marks a block whose real output needs a live CI system
	// and a git host; the book says so instead of showing invented output.
	outputToCome = "**Output to come.**"
)

var bookDirs = []string{"docs/introduction", "docs/concepts", "docs/tutorial", "docs/how-to", "docs/reference", "docs/worked-example"}

func docPages(t *testing.T, root, dir string) []string {
	t.Helper()

	var pages []string

	err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}

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

	return pages
}

func bookPages(t *testing.T, root string) []string {
	t.Helper()

	pages := []string{"README.md", "docs/README.md", "docs/glossary.md", "docs/troubleshooting.md"}
	for _, d := range bookDirs {
		pages = append(pages, docPages(t, root, d)...)
	}

	return pages
}

type fence struct {
	info  string
	lines []string
	line  int // the line of the opening fence, from 1
}

func fences(text string) []fence {
	var (
		out []fence
		cur *fence
	)

	for i, l := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(l)

		switch {
		case cur == nil && strings.HasPrefix(trimmed, "```"):
			cur = &fence{info: strings.TrimPrefix(trimmed, "```"), line: i + 1}
		case cur != nil && trimmed == "```":
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			cur.lines = append(cur.lines, l)
		}
	}

	return out
}

var summaryLinkRE = regexp.MustCompile(`\]\(([^)#\s]+\.md)\)`)

func TestSummaryListsEveryPage(t *testing.T) {
	root := repoRoot(t)
	summary := read(t, filepath.Join(root, summaryPage))

	listed := map[string]bool{}
	for _, m := range summaryLinkRE.FindAllStringSubmatch(summary, -1) {
		listed[filepath.ToSlash(filepath.Clean(filepath.Join("docs", m[1])))] = true
	}

	for _, p := range docPages(t, root, "docs") {
		if p != summaryPage && !listed[p] {
			t.Errorf("%s doesn't list %s: add it where it belongs in the book's order", summaryPage, p)
		}
	}
}

// commandLineRE finds pipelinegen called in a line of a code block or an
// inline code span: at the start, after a prompt, after an environment
// assignment, or as tools/pipelinegen. Output that mentions pipelinegen in
// a sentence ("this pipelinegen plans by links") is no call.
var commandLineRE = regexp.MustCompile(`(?:^\s*|\$ |=\S+\s+|tools/)pipelinegen\s+([a-z-]+)((?:\s+\S+)*)`)

var inlineCodeRE = regexp.MustCompile("`([^`\n]+)`")

func TestCommandsShownExist(t *testing.T) {
	root := repoRoot(t)

	cmds := map[string]cli.CommandDoc{}
	for _, c := range cli.Commands() {
		cmds[c.Name] = c
	}

	flagsOf := func(docs []cli.FlagDoc) []string {
		names := []string{"h", "help"}
		for _, f := range docs {
			names = append(names, f.Name)
		}

		return names
	}

	check := func(page, where, snippet string) {
		for _, m := range commandLineRE.FindAllStringSubmatch(snippet, -1) {
			name, args := m[1], strings.Fields(m[2])
			for i, a := range args {
				args[i] = strings.TrimRight(a, "`.,;:)")
			}

			// pipelinegen --help lists the commands
			if strings.HasPrefix(name, "-") && slices.Contains([]string{"-h", "-help", "--help"}, name) {
				continue
			}

			c, ok := cmds[name]
			if !ok {
				t.Errorf("%s %s: `pipelinegen %s` isn't a command", page, where, name)

				continue
			}

			flags := c.Flags

			if len(c.Subcommands) > 0 {
				i := slices.IndexFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
				if i >= 0 && !strings.ContainsAny(args[i], "<$\"'") {
					j := slices.IndexFunc(c.Subcommands, func(s cli.CommandDoc) bool { return s.Name == args[i] })
					if j < 0 {
						t.Errorf("%s %s: `pipelinegen %s %s` isn't a subcommand", page, where, name, args[i])

						continue
					}

					flags = c.Subcommands[j].Flags
				}
			}

			for _, a := range args {
				if !strings.HasPrefix(a, "-") || a == "-" || a == "--" || strings.ContainsAny(a, "<[") {
					continue
				}

				f, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")

				if !slices.Contains(flagsOf(flags), f) {
					t.Errorf("%s %s: `pipelinegen %s` has no flag --%s", page, where, name, f)
				}
			}
		}
	}

	for _, p := range bookPages(t, root) {
		text := read(t, filepath.Join(root, p))

		for _, f := range fences(text) {
			for i, l := range f.lines {
				// a shell pipeline or an && chain holds several commands
				for part := range strings.SplitSeq(l, "|") {
					for cmd := range strings.SplitSeq(part, "&&") {
						check(p, fmt.Sprintf("line %d", f.line+1+i), cmd)
					}
				}
			}
		}

		for _, m := range inlineCodeRE.FindAllStringSubmatch(text, -1) {
			if strings.HasPrefix(m[1], "pipelinegen ") {
				check(p, "inline", m[1])
			}
		}
	}
}

type configKeys struct{ known map[string]bool }

func newConfigKeys() configKeys {
	k := configKeys{known: map[string]bool{}}

	for _, d := range config.Keys() {
		p := d.Path
		k.known[p] = true

		for i := range len(p) {
			if p[i] == '.' || p[i] == '[' {
				k.known[p[:i]] = true
			}
		}
	}

	return k
}

func (k configKeys) branches(path string) bool {
	for p := range k.known {
		if strings.HasPrefix(p, path+".") || strings.HasPrefix(p, path+"[]") {
			return true
		}
	}

	return false
}

func join(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

func (k configKeys) walk(n *yaml.Node, path string, bad func(string)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			k.walk(c, path, bad)
		}
	case yaml.MappingNode:
		if path != "" && !k.branches(path) {
			return
		}

		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value

			p := join(path, key)
			if !k.known[p] {
				p = join(path, "<name>")
			}

			if !k.known[p] {
				bad(join(path, key))

				continue
			}

			k.walk(n.Content[i+1], p, bad)
		}
	case yaml.SequenceNode:
		if !k.branches(path) {
			return
		}

		for _, c := range n.Content {
			k.walk(c, path+"[]", bad)
		}
	case yaml.ScalarNode, yaml.AliasNode:
		// a value: its key was checked on the way in
	}
}

func TestConfigKeysShownExist(t *testing.T) {
	root := repoRoot(t)
	keys := newConfigKeys()
	snippets := 0

	for _, p := range bookPages(t, root) {
		for _, f := range fences(read(t, filepath.Join(root, p))) {
			if f.info != "yaml" {
				continue
			}

			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(strings.Join(f.lines, "\n")), &doc); err != nil {
				t.Errorf("%s line %d: the YAML doesn't parse: %v", p, f.line, err)

				continue
			}

			// only snippets of pipelinegen.yaml: a mapping with a top-level
			// key of the config (task files and plugin configs are others)
			if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
				continue
			}

			top := doc.Content[0]
			isConfig := false

			for i := 0; i < len(top.Content); i += 2 {
				isConfig = isConfig || keys.known[top.Content[i].Value]
			}

			if !isConfig {
				continue
			}

			snippets++

			keys.walk(&doc, "", func(path string) {
				t.Errorf("%s line %d: `%s` isn't a key of pipelinegen.yaml", p, f.line, path)
			})
		}
	}

	if snippets < 10 {
		t.Fatalf("found only %d config snippets in the book", snippets)
	}
}

var (
	releasePinRE  = regexp.MustCompile(`version: (v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?), sha256: ([0-9a-f]{64})`)
	downloadRE    = regexp.MustCompile(`releases/download/(v[0-9][^/\s]*)/([a-z-]+?)-linux-(?:amd64|arm64)`)
	fileSumRE     = regexp.MustCompile(`([0-9a-f]{64})  tools/plugins/([a-z-]+)`)
	versionVarRE  = regexp.MustCompile(`VERSION=(v[0-9][^\s]*)`)
	pinnedSamples = []string{"example-notes/pipelinegen.yaml", "example/pipelinegen.yaml"}
)

func TestPinsShownAreTheExamples(t *testing.T) {
	root := repoRoot(t)

	sums := map[string]string{}     // version+sha256 pairs the examples pin
	byRepo := map[string]string{}   // the program a repository holds: its pinned version
	byPlugin := map[string]string{} // a plugin entry's name: its sha256

	for _, s := range pinnedSamples {
		cfg, err := config.Load(filepath.Join(root, s))
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range cfg.Plugins {
			if p.Release == nil {
				continue
			}

			sums[p.Release.Version+" "+p.Release.SHA256] = s
			byRepo[p.Program()] = p.Release.Version
			byPlugin[p.Name] = p.Release.SHA256
		}

		if r := cfg.Concourse.Planner.Bootstrap.Release; r != nil {
			sums[r.Version+" "+r.SHA256] = s
			byRepo["pipelinegen"] = r.Version
		}

		if b := cfg.Concourse.Planner.Bootstrap.Bazelisk; b.Version != "" {
			sums[b.Version+" "+b.SHA256] = s
		}
	}

	for _, p := range bookPages(t, root) {
		// the worked example shows the pins its conversion was measured
		// with, and sends the reader to the book's for the current ones
		if strings.HasPrefix(p, "docs/worked-example/") {
			continue
		}

		text := read(t, filepath.Join(root, p))

		for _, m := range releasePinRE.FindAllStringSubmatch(text, -1) {
			if _, ok := sums[m[1]+" "+m[2]]; !ok {
				t.Errorf("%s pins %s with a checksum no example pins: use the pins of %v", p, m[1], pinnedSamples)
			}
		}

		for _, m := range downloadRE.FindAllStringSubmatch(text, -1) {
			if v, ok := byRepo[m[2]]; ok && v != m[1] {
				t.Errorf("%s downloads %s %s; the examples pin %s", p, m[2], m[1], v)
			}
		}

		for _, m := range fileSumRE.FindAllStringSubmatch(text, -1) {
			if s, ok := byPlugin[m[2]]; ok && s != m[1] {
				t.Errorf("%s checks tools/plugins/%s against another checksum than the examples pin", p, m[2])
			}
		}

		for _, m := range versionVarRE.FindAllStringSubmatch(text, -1) {
			if m[1] != byRepo["pipelinegen"] {
				t.Errorf("%s installs pipelinegen %s; the examples pin %s", p, m[1], byRepo["pipelinegen"])
			}
		}
	}
}

var outputMarkRE = regexp.MustCompile("(?s)<!-- output: ([a-z0-9-]+) -->\n```[a-z]*\n(.*?)```\n")

func TestTranscriptsAreShownAsRun(t *testing.T) {
	root := repoRoot(t)

	entries, err := os.ReadDir(filepath.Join(root, transcriptsDir))
	if err != nil {
		t.Fatal(err)
	}

	shown := map[string]bool{}
	toCome := 0

	for _, p := range append([]string{"README.md"}, docPages(t, root, "docs")...) {
		text := read(t, filepath.Join(root, p))
		toCome += strings.Count(text, outputToCome)

		for _, m := range outputMarkRE.FindAllStringSubmatch(text, -1) {
			shown[m[1]] = true

			want, err := os.ReadFile(filepath.Join(root, transcriptsDir, m[1]+".txt"))
			if err != nil {
				t.Errorf("%s shows the output %s, which has no transcript: %v", p, m[1], err)

				continue
			}

			if m[2] != string(want) {
				t.Errorf("%s: the output %s differs from %s/%s.txt", p, m[1], transcriptsDir, m[1])
			}
		}
	}

	for _, e := range entries {
		if name := strings.TrimSuffix(e.Name(), ".txt"); !shown[name] {
			t.Errorf("%s/%s isn't shown on any page", transcriptsDir, e.Name())
		}
	}

	t.Logf("%d places in the book still wait for output from a live run (%q)", toCome, outputToCome)
}
