package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Filler fills the caches plugins declare before they answer on a person's machine, where no
// planner prepare filled them; only the local commands use one.
type Filler struct {
	Who string
	Out io.Writer

	mu  sync.Mutex
	ran map[string]bool
}

type fillerKey struct{}

// WithFiller has the plugin sets planning under ctx fill their caches with f before answering.
func WithFiller(ctx context.Context, f *Filler) context.Context {
	return context.WithValue(ctx, fillerKey{}, f)
}

func fillerFrom(ctx context.Context) *Filler {
	f, _ := ctx.Value(fillerKey{}).(*Filler) //nolint:errcheck // absent is no filler

	return f
}

const fillMarks = ".filled"

// FillProblem is what is wrong with a fill as describe declares it, empty when nothing is.
func FillProblem(cache string, f plugin.Fill) string {
	switch {
	case len(f.Run) == 0 || f.Run[0] == "":
		return fmt.Sprintf("fill of %s names no program", cache)
	case strings.ContainsAny(f.Run[0], `/\`):
		return fmt.Sprintf("fill of %s runs %q: a program found on PATH, never a path", cache, f.Run[0])
	case len(f.Files) == 0:
		return fmt.Sprintf("fill of %s names no files", cache)
	case secret.CacheVars[cache] == "":
		return fmt.Sprintf("fill of %s: no cache of that name the core points a variable at", cache)
	}

	for _, name := range f.Files {
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return fmt.Sprintf("fill of %s: file %q is no file name", cache, name)
		}
	}

	return ""
}

func (s *Set) filledBlobs(ctx context.Context, root string) (map[string]string, error) {
	files, err := blobs(ctx, root)
	if err == nil {
		if f := fillerFrom(ctx); f != nil {
			s.fill(ctx, f, root, files)
		}
	}

	return files, err
}

func (s *Set) fill(ctx context.Context, f *Filler, root string, files map[string]string) {
	type job struct {
		cache    string
		spec     plugin.Fill
		readers  []string
		excludes map[string][]string
	}

	var jobs []*job

	for _, l := range s.Plugins {
		if !l.Conf.Fills() {
			continue
		}

		for _, cache := range sortedKeys(l.Desc.Fill) {
			spec := l.Desc.Fill[cache]
			if p := FillProblem(cache, spec); p != "" {
				s.warnf("plugin %s: %s; not filled", l.Conf.Name, p)

				continue
			}

			i := slices.IndexFunc(jobs, func(j *job) bool {
				return j.cache == cache && slices.Equal(j.spec.Run, spec.Run) && slices.Equal(j.spec.Files, spec.Files)
			})
			if i < 0 {
				jobs = append(jobs, &job{cache: cache, spec: spec, excludes: map[string][]string{}})
				i = len(jobs) - 1
			}

			jobs[i].readers = append(jobs[i].readers, l.Conf.Name)
			jobs[i].excludes[l.Conf.Name] = l.Conf.Exclude
		}
	}

	caches := filepath.Join(s.o.Cache, "caches")

	for _, j := range jobs {
		for _, dir := range fillDirs(files, j.spec.Files[0]) {
			// a directory whose file every reader leaves out (an example workspace, say) is read by
			// no plugin: filling it changes no answer
			readers := slices.DeleteFunc(slices.Clone(j.readers), func(name string) bool {
				return matchAny(j.excludes[name], path.Join(dir, j.spec.Files[0]))
			})
			if len(readers) == 0 {
				continue
			}

			f.one(ctx, s, root, caches, dir, j.cache, j.spec, readers, files)
		}
	}
}

func fillDirs(files map[string]string, name string) []string {
	var out []string

	for p := range files {
		if path.Base(p) == name {
			out = append(out, path.Dir(p))
		}
	}

	slices.Sort(out)

	return out
}

func (f *Filler) one(ctx context.Context, s *Set, root, caches, dir, cache string, spec plugin.Fill, readers []string, files map[string]string) {
	parts := make([]string, 0, 2+2*len(spec.Files))
	parts = append(parts, cache, strings.Join(spec.Run, "\x00"))

	for _, name := range spec.Files {
		parts = append(parts, name, files[path.Join(dir, name)])
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	key := hex.EncodeToString(sum[:])
	mark := filepath.Join(caches, fillMarks, cache, key)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.ran == nil {
		f.ran = map[string]bool{}
	}

	if f.ran[key] {
		return
	}

	if _, err := os.Stat(mark); err == nil {
		f.ran[key] = true

		return
	}

	target := filepath.Join(caches, cache)
	where := filepath.Join(root, filepath.FromSlash(dir))
	command := strings.Join(spec.Run, " ")

	f.sayf("pipelinegen %s: filling the %s cache for %s: %s in %s, into %s\n", f.Who, cache, strings.Join(readers, ", "), command, dir, target)

	out, err := RunFill(ctx, where, target, cache, spec)
	if err != nil {
		msg := fmt.Sprintf("filling the %s cache failed: `%s` in %s: %v: %s; %s may not tell what depends on what it lacks, and bind wide there; "+
			"the fill runs without tokens from the environment: use a git credential helper or netrc, or run the command yourself",
			cache, command, dir, err, secret.Redact(lastLines(string(out), 3)), strings.Join(readers, " and "))
		f.sayf("pipelinegen %s: %s\n", f.Who, msg)
		s.warnf("%s", msg)

		return
	}

	f.ran[key] = true

	if err := os.MkdirAll(filepath.Dir(mark), 0o750); err == nil {
		_ = os.WriteFile(mark, []byte(command+" in "+dir+"\n"), 0o600) //nolint:errcheck // without the mark it runs again next time, which is all
	}
}

// RunFill runs a cache's fill in dir with target as the cache: argv only, never a shell, in the
// person's environment less every secret and pipelinegen's own variables.
func RunFill(ctx context.Context, dir, target, cache string, spec plugin.Fill) ([]byte, error) {
	if p := FillProblem(cache, spec); p != "" {
		return nil, fmt.Errorf("%w: %s", ErrPlugins, p)
	}

	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlugins, err)
	}

	cmd := exec.CommandContext(ctx, spec.Run[0], spec.Run[1:]...) //nolint:gosec // a program the plugin's describe names, run as the person who asked for the plan
	cmd.Dir = dir
	cmd.Env = secret.FillEnv(map[string]string{secret.CacheVars[cache]: target})

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w", spec.Run[0], err)
	}

	return out, nil
}

func (f *Filler) sayf(format string, args ...any) {
	if f.Out == nil {
		return
	}

	if _, err := fmt.Fprintf(f.Out, format, args...); err != nil {
		return
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "; ")
}
