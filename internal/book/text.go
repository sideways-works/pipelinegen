// Package book checks that the book shows what the tool prints: it runs the guide on a fresh copy
// of example-notes, trims each output and compares it with docs/transcripts, or fills them.
package book

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/plugkit/pin"
)

// splitLines splits text at line boundaries as Python's str.splitlines does: \n, \r\n, \r and the
// other boundaries Python counts (\v, \f, \x1c-\x1e, \x85,  ,  ).
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var (
		lines []string
		line  strings.Builder
	)

	for _, r := range text {
		switch r {
		case '\n', '\r', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', ' ', ' ':
			lines = append(lines, line.String())
			line.Reset()
		default:
			line.WriteRune(r)
		}
	}

	if line.Len() > 0 {
		lines = append(lines, line.String())
	}

	return lines
}

var (
	pluginsLine   = regexp.MustCompile(`^plugins: \d+ of \d+ files`)
	localRun      = regexp.MustCompile(`local-\d{8}-\d{6}-\d+`)
	totalActions  = regexp.MustCompile(`, \d+ total actions?$`)
	scratchRemote = regexp.MustCompile(`file://\S+/remote`)
	// a long step's progress, which a slower machine prints more of.
	progress = regexp.MustCompile(`^pipelinegen \w+: .*, (\d+h)?(\d+m)?\d+(\.\d+)?s so far$`)
	// bazelisk's download of Bazel, stamped with the time it ran.
	bazeliskLine = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (Downloading https://|Signed by )`)
	// how long a driver's tool took: go test's package and test lines, Playwright's summary and
	// npm's install.
	toolTime = regexp.MustCompile(`(ok  \t\S+\t|--- (?:PASS|FAIL|SKIP): \S+ \(|passed \(|packages in )\d+(\.\d+)?m?s`)
)

func trimmed(name, raw, work string) string {
	work = strings.TrimRight(work, "/") + "/"

	var keep []string

	skipPlugins := false

	for _, l := range splitLines(raw) {
		if l == "[exit 0]" {
			continue
		}

		if strings.HasPrefix(l, "plugins: ") && pluginsLine.MatchString(l) {
			skipPlugins = true

			continue
		}

		if skipPlugins && strings.HasPrefix(l, "  ") {
			continue
		}

		skipPlugins = false

		// what the go driver downloads depends on what its module cache held, and npm's notices on
		// what the registry has published since
		if strings.HasPrefix(l, "go run: go: downloading ") || strings.HasPrefix(l, "playwright run: npm notice") || progress.MatchString(l) || bazeliskLine.MatchString(l) ||
			strings.HasPrefix(l, "... still trying to connect to local Bazel server") {
			continue
		}

		if strings.HasPrefix(l, "$TEST_TMPDIR defined") || strings.TrimSpace(l) == "no actions running" ||
			strings.HasPrefix(l, "Starting local Bazel server") || strings.HasPrefix(l, "Extracting Bazel installation") {
			continue
		}

		// a local recording's run is named by the time it ran
		l = localRun.ReplaceAllString(l, "local-<time>")
		if strings.HasPrefix(l, "go run: ") || strings.HasPrefix(l, "playwright run: ") {
			l = toolTime.ReplaceAllString(l, "${1}<time>")
		}
		// the steps keep pipelinegen's user cache in the work directory
		l = strings.ReplaceAll(strings.ReplaceAll(l, work+"xdg/", "/home/you/.cache/"), work, "/home/you/")
		keep = append(keep, l)
	}

	switch name {
	case "gazelle.txt":
		// how many actions ran depends on what Bazel's cache already held
		out := []string{first(keep), "..."}

		for _, l := range keep {
			if strings.HasPrefix(l, "INFO: Build completed") || strings.HasPrefix(l, "INFO: Running command line") {
				out = append(out, totalActions.ReplaceAllString(l, ""))
			}
		}

		keep = out
	case "query.txt":
		keep = slices.DeleteFunc(keep, func(l string) bool { return strings.HasPrefix(l, "Loading:") })
	case "record.txt":
		// the path of the scratch directory differs per run
		for i, l := range keep {
			keep[i] = scratchRemote.ReplaceAllString(l, "file:///tmp/tmp.XXXXXXXXXX/remote")
		}

		if len(keep) > 0 {
			keep[0] = "$ record-locally.sh ~/src/pipelinegen owner/notes go=ci/go-batch.sh e2e=ci/e2e-batch.sh"
		}
	}

	return strings.Join(keep, "\n") + "\n"
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}

	return list[0]
}

// Trim writes each raw output of raw into out as the book shows it; work is the scratch
// directory the steps ran in.
func Trim(raw, out, work string) error {
	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("trim: %w", err)
	}

	entries, err := os.ReadDir(raw)
	if err != nil {
		return fmt.Errorf("trim: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".txt") || name == "pipeline-jobs-count.txt" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(raw, name)) //nolint:gosec // the steps' own output
		if err != nil {
			return fmt.Errorf("trim: %w", err)
		}

		if err := os.WriteFile(filepath.Join(out, name), []byte(trimmed(name, string(data), work)), 0o644); err != nil { //nolint:gosec // a transcript of the book
			return fmt.Errorf("trim: %w", err)
		}
	}

	return nil
}

var outputBlock = regexp.MustCompile("(?s)(<!-- output: ([A-Za-z0-9_-]+) -->\n```[a-z]*\n)(.*?)(```\n)")

// Fill puts each transcript into the fenced block after its <!-- output: NAME --> marker in the
// pages given, or every page under docs; it returns the pages it changed.
func Fill(docs, transcripts string, pages ...string) ([]string, error) {
	if len(pages) == 0 {
		err := filepath.WalkDir(docs, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				pages = append(pages, p)
			}

			return err
		})
		if err != nil {
			return nil, fmt.Errorf("fill: %w", err)
		}
	}

	var filled []string

	for _, p := range pages {
		data, err := os.ReadFile(p) //nolint:gosec // a page of the book
		if err != nil {
			return filled, fmt.Errorf("fill: %w", err)
		}

		var missing error

		text := outputBlock.ReplaceAllStringFunc(string(data), func(block string) string {
			m := outputBlock.FindStringSubmatch(block)

			t, err := os.ReadFile(filepath.Join(transcripts, m[2]+".txt")) //nolint:gosec // a transcript of the book
			if err != nil {
				missing = err

				return block
			}

			return m[1] + string(t) + m[4]
		})

		if missing != nil {
			return filled, fmt.Errorf("fill %s: %w", p, missing)
		}

		if text != string(data) {
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil { //nolint:gosec // a page of the book
				return filled, fmt.Errorf("fill: %w", err)
			}

			filled = append(filled, p)
		}
	}

	return filled, nil
}

// sumsSuffix names the release's SHA256SUMS kept beside a plugin pinned by sums, as pipelinegen
// reads it.
const sumsSuffix = ".SHA256SUMS"

var (
	bootstrapRelease = regexp.MustCompile(`bootstrap:\s*\n\s*release: \{version: (\S+), sha256`)
	pinnedPlugin     = regexp.MustCompile(`- (?:\{name: ([^,\s]+),[^\n]*?|name: (\S+)\n(?:[ \t]+\S.*\n)*?[ \t]+)release: \{version: \S+, (sha256|sums): ([0-9a-f]{64})\}`)
)

func sumOf(data []byte) string {
	s := sha256.Sum256(data)

	return hex.EncodeToString(s[:])
}

// PinProblems are what keeps the outputs from being the pinned releases': the pipelinegen on PATH
// must be the version config pins, and each plugin in plugins its pinned binary, beside the
// SHA256SUMS a pin by sums names.
func PinProblems(config, plugins, version string, read func(string) ([]byte, error)) []string {
	var bad []string

	boot := bootstrapRelease.FindStringSubmatch(config)

	switch {
	case boot == nil:
		bad = append(bad, "example-notes pins no pipelinegen release")
	case version != boot[1]:
		bad = append(bad, fmt.Sprintf("pipelinegen on PATH is %s; example-notes pins %s", version, boot[1]))
	}

	sum := func(path string) string {
		data, err := read(path)
		if err != nil {
			return "missing"
		}

		return sumOf(data)
	}

	for _, m := range pinnedPlugin.FindAllStringSubmatch(config, -1) {
		path, by, want := filepath.Join(plugins, m[1]+m[2]), m[3], m[4]

		if by == "sha256" {
			if got := sum(path); got != want {
				bad = append(bad, fmt.Sprintf("%s: %s, the pin is %s", path, got, want))
			}

			continue
		}

		list := path + sumsSuffix
		if got := sum(list); got != want {
			bad = append(bad, fmt.Sprintf("%s: %s, the pin is %s", list, got, want))

			continue
		}

		data, _ := read(list) //nolint:errcheck // read once already, its checksum the pin's
		if got := sum(path); !slices.Contains(slices.Collect(maps.Values(pin.Listed(data))), got) {
			bad = append(bad, fmt.Sprintf("%s: %s, which %s doesn't list", path, got, list))
		}
	}

	return bad
}
