package docscheck

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

const (
	narrowingPage = "docs/how-to/narrowing.md"
	designPage    = "docs/design.md"
)

var (
	guideRowRE  = regexp.MustCompile("^\\|\\s*`([a-z][a-z0-9-]*)`\\s*\\|.*\\|\\s*([0-9])\\s*\\|\\s*$")
	designRowRE = regexp.MustCompile(`^\|\s*([^|]+?)\s*\|\s*([0-9])\s*\|`)
	nameRE      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func guideLevels(t *testing.T, page string) map[string]int {
	t.Helper()

	out := map[string]int{}

	for line := range strings.SplitSeq(page, "\n") {
		if m := guideRowRE.FindStringSubmatch(line); m != nil {
			out[m[1]] = atoi(t, m[2])
		}
	}

	return out
}

func designLevels(t *testing.T, page string) map[string]int {
	t.Helper()

	out := map[string]int{}

	_, ladder, ok := strings.Cut(page, "\n### The ladder\n")
	if !ok {
		t.Fatalf("%s has no ladder", designPage)
	}

	ladder, _, _ = strings.Cut(ladder, "\n### ")

	for line := range strings.SplitSeq(ladder, "\n") {
		m := designRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		names := strings.Split(m[1], ", ")

		plain := true
		for _, n := range names {
			plain = plain && nameRE.MatchString(n)
		}

		if !plain {
			continue
		}

		for _, n := range names {
			out[n] = atoi(t, m[2])
		}
	}

	return out
}

func atoi(t *testing.T, s string) int {
	t.Helper()

	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}

	return n
}

func TestNarrowingTablesAgree(t *testing.T) {
	root := repoRoot(t)
	pages := map[string]map[string]int{
		narrowingPage: guideLevels(t, read(t, filepath.Join(root, narrowingPage))),
		designPage:    designLevels(t, read(t, filepath.Join(root, designPage))),
	}

	code := map[string]int{}

	for _, n := range plugins.CoreNarrowings {
		code[n.Name] = n.OffAt
	}

	for page, levels := range pages {
		if len(levels) == 0 {
			t.Fatalf("no rows read from %s", page)
		}

		for name, level := range code {
			if got, ok := levels[name]; !ok {
				t.Errorf("the code declares %s (off from %d), which %s lacks", name, level, page)
			} else if got != level {
				t.Errorf("%s: off from %d in the code, %d in %s", name, level, got, page)
			}
		}

		for name := range levels {
			if _, ok := code[name]; !ok {
				t.Errorf("%s lists %s, which the core doesn't declare: a plugin's narrowings belong to its own docs", page, name)
			}
		}
	}
}
