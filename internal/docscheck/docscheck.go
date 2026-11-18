// Package docscheck reads the documentation as its test needs: a reference page's sections,
// whether each describes code on main, and the links between pages.
package docscheck

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// NotReleased is the line a reference section starts with while the code it describes is designed
// but not on main.
const NotReleased = "Not in a release yet."

type Section struct {
	Heading string
	Body    string
}

func (s Section) Released() bool {
	return !strings.HasPrefix(strings.TrimSpace(s.Body), NotReleased)
}

// Sections splits a Markdown page at its headings of the given level (2 for "## "), skipping
// fenced code.
func Sections(page string, level int) []Section {
	marker := strings.Repeat("#", level) + " "

	var (
		out   []Section
		cur   *Section
		body  strings.Builder
		fence bool
	)

	flush := func() {
		if cur != nil {
			cur.Body = body.String()
			out = append(out, *cur)
		}

		body.Reset()
	}

	for line := range strings.SplitSeq(page, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
		}

		if !fence && strings.HasPrefix(line, marker) {
			flush()

			cur = &Section{Heading: strings.TrimSpace(strings.TrimPrefix(line, marker))}

			continue
		}

		body.WriteString(line + "\n")
	}

	flush()

	return out
}

type Link struct {
	Page   string
	Target string
	Line   int
}

var (
	linkRE   = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	inlineRE = regexp.MustCompile("`[^`]*`")
	schemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)
)

// Links lists the links of a Markdown page that point into the repository: no scheme, outside
// code.
func Links(page, text string) []Link {
	var (
		out   []Link
		fence bool
	)

	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence

			continue
		}

		if fence {
			continue
		}

		line = inlineRE.ReplaceAllString(line, "")

		for _, m := range linkRE.FindAllStringSubmatch(line, -1) {
			if schemeRE.MatchString(m[1]) {
				continue
			}

			out = append(out, Link{Page: page, Target: m[1], Line: i + 1})
		}
	}

	return out
}

// Anchors lists the anchors GitHub gives a page's headings: lower case, punctuation dropped,
// spaces as dashes, and -1, -2 for repeats.
func Anchors(text string) map[string]bool {
	out := map[string]bool{}
	fence := false

	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence

			continue
		}

		if fence || !strings.HasPrefix(line, "#") {
			continue
		}

		h := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if h == "" || !strings.HasPrefix(strings.TrimLeft(line, "#"), " ") {
			continue
		}

		slug := slugify(h)

		a := slug
		for n := 1; out[a]; n++ {
			a = slug + "-" + strconv.Itoa(n)
		}

		out[a] = true
	}

	return out
}

func slugify(h string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(h) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}

	return b.String()
}

var ErrLink = errors.New("dead link")

// Resolve checks one link of a page under root: the file or directory it names must exist, and
// for an anchor into a Markdown page, the heading.
func Resolve(root string, l Link) error {
	target, anchor, _ := strings.Cut(l.Target, "#")

	file := l.Page
	if target != "" {
		file = path.Join(path.Dir(l.Page), target)
	}

	if file == ".." || strings.HasPrefix(file, "../") {
		return fmt.Errorf("%w: %s:%d: %s leaves the repository", ErrLink, l.Page, l.Line, l.Target)
	}

	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return fmt.Errorf("%w: %s:%d: %s: no such file", ErrLink, l.Page, l.Line, l.Target)
	}

	if anchor == "" || st.IsDir() || !strings.HasSuffix(file, ".md") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file))) //nolint:gosec // a page of the repository
	if err != nil {
		return fmt.Errorf("%w: %s:%d: %w", ErrLink, l.Page, l.Line, err)
	}

	if !Anchors(string(data))[anchor] {
		return fmt.Errorf("%w: %s:%d: %s: no heading #%s", ErrLink, l.Page, l.Line, l.Target, anchor)
	}

	return nil
}
