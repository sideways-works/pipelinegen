package book

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// notesPort is the port the notes server of the book's guide listens on,
// unless NOTES_PORT names another: the guide's copy of the config has it
// in place of the 8080 the book shows.
const notesPort = "7181"

type guide struct {
	src, work, out, plugins, notes, port string
	env                                  []string
	commits                              int
}

// ErrStep is a step the guide needs that failed.
var ErrStep = errors.New("a step failed")

// Steps runs the guide: book is the tool's checkout, whose example-notes
// it copies; work is where it runs and out where the outputs go. It needs
// pipelinegen, bazel (bazelisk), go, node and a Playwright browser on
// PATH, and the pinned plugins in plugins.
func Steps(ctx context.Context, book, work, out, plugins string) error {
	g := &guide{src: filepath.Join(book, "example-notes"), work: work, out: out, plugins: plugins, notes: filepath.Join(work, "notes")}

	// Go makes its module cache read-only; make it removable first
	if _, err := os.Stat(work); err == nil {
		_ = filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // what stays read-only fails the removal below
			if err == nil {
				if info, err := d.Info(); err == nil {
					_ = os.Chmod(p, info.Mode().Perm()|0o200) //nolint:errcheck,gosec // as chmod -R u+w on the guide's own work
				}
			}

			return nil
		})
	}

	for _, d := range []string{work, out} {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("steps: %w", err)
		}

		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("steps: %w", err)
		}
	}

	npm := os.Getenv("npm_config_cache")
	if npm == "" {
		npm = filepath.Join(work, "npm")
	}

	// pipelinegen's own cache, fresh for every run, as on a new machine;
	// the browsers Playwright installed stay where they are
	browsers := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if browsers == "" {
		cache := os.Getenv("XDG_CACHE_HOME")
		if cache == "" {
			cache = filepath.Join(os.Getenv("HOME"), ".cache")
		}

		browsers = filepath.Join(cache, "ms-playwright")
	}

	port := os.Getenv("NOTES_PORT")
	if port == "" {
		port = notesPort
	}

	g.port = port
	g.env = setEnv(os.Environ(),
		"GIT_AUTHOR_NAME=you", "GIT_AUTHOR_EMAIL=you@example.com", "GIT_COMMITTER_NAME=you", "GIT_COMMITTER_EMAIL=you@example.com",
		"NOTES_PORT="+port, "npm_config_cache="+npm, "PLAYWRIGHT_BROWSERS_PATH="+browsers, "XDG_CACHE_HOME="+filepath.Join(work, "xdg"),
	)

	return g.run(ctx)
}

func setEnv(env []string, kv ...string) []string {
	out := slices.Clone(env)

	for _, s := range kv {
		name, _, _ := strings.Cut(s, "=")
		out = slices.DeleteFunc(out, func(e string) bool { return strings.HasPrefix(e, name+"=") })
		out = append(out, s)
	}

	return out
}

func (g *guide) sh(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the guide's own commands
	cmd.Dir = g.notes
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

func (g *guide) git(ctx context.Context, args ...string) error {
	return g.sh(ctx, g.env, "git", args...)
}

// commit commits what is staged at the next minute of a fixed day, so
// every run makes the same commits.
func (g *guide) commit(ctx context.Context, message string) error {
	g.commits++
	date := fmt.Sprintf("2026-01-01T12:%02d:00Z", g.commits)

	return g.sh(ctx, setEnv(g.env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date), "git", "commit", "-qm", message)
}

func (g *guide) commitAll(ctx context.Context, message string) error {
	if err := g.git(ctx, "add", "-A"); err != nil {
		return err
	}

	return g.commit(ctx, message)
}

func (g *guide) commitTracked(ctx context.Context, message string) error {
	if err := g.git(ctx, "add", "-u"); err != nil {
		return err
	}

	return g.commit(ctx, message)
}

func (g *guide) step(ctx context.Context, name, line string) error {
	f, err := os.Create(filepath.Join(g.out, name+".txt")) //nolint:gosec // the guide's own output
	if err != nil {
		return fmt.Errorf("step %s: %w", name, err)
	}

	defer func() { _ = f.Close() }() //nolint:errcheck // written whole by now

	if _, err := fmt.Fprintf(f, "$ %s\n", line); err != nil {
		return fmt.Errorf("step %s: %w", name, err)
	}

	// as the guide's script ran it: nounset and pipefail on, errexit off
	cmd := exec.CommandContext(ctx, "bash", "-uo", "pipefail", "-c", line) //nolint:gosec // the book's own command line
	cmd.Dir = g.notes
	cmd.Env = g.env
	cmd.Stdout, cmd.Stderr = f, f

	code := 0

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return fmt.Errorf("step %s: %w", name, err)
		}

		code = exit.ExitCode()
	}

	if _, err := fmt.Fprintf(f, "[exit %d]\n", code); err != nil {
		return fmt.Errorf("step %s: %w", name, err)
	}

	return nil
}

func (g *guide) edit(file string, line func(string) string) error {
	path := filepath.Join(g.notes, filepath.FromSlash(file))

	data, err := os.ReadFile(path) //nolint:gosec // the notes service's own file
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = line(lines[i])
	}

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil { //nolint:gosec // a source file
		return fmt.Errorf("edit: %w", err)
	}

	return nil
}

func sub(old, repl string) func(string) string {
	return func(l string) string { return strings.Replace(l, old, repl, 1) }
}

func subRe(re, repl string) func(string) string {
	r := regexp.MustCompile(re)

	return func(l string) string {
		done := false

		return r.ReplaceAllStringFunc(l, func(m string) string {
			if done {
				return m
			}

			done = true

			return repl
		})
	}
}

func (g *guide) write(file, text string) error {
	path := filepath.Join(g.notes, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil { //nolint:gosec // a file of the notes service
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

func (g *guide) appendTo(file, text string) error {
	f, err := os.OpenFile(filepath.Join(g.notes, filepath.FromSlash(file)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // a file of the notes service
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}

	_, err = f.WriteString(text)

	return errors.Join(err, f.Close())
}

func (g *guide) copyFile(from, to string, mode os.FileMode) error {
	data, err := os.ReadFile(from) //nolint:gosec // the book's own files and the pinned plugins
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	path := filepath.Join(g.notes, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := os.WriteFile(path, data, mode); err != nil { //nolint:gosec // the guide's own copy
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func (g *guide) wordsEdit() error {
	return g.edit("text/text.go", sub("return !unicode.IsLetter(r) && !unicode.IsDigit(r)", "return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-')"))
}

func (g *guide) head(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = g.notes
	cmd.Env = g.env

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

func (g *guide) reset(ctx context.Context, to string) error {
	return g.git(ctx, "reset", "-q", "--hard", to)
}
