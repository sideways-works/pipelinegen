package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/plugkit/fetch"
	"github.com/sideways-works/plugkit/pin"
	"github.com/sideways-works/plugkit/store"
)

var errCommitPin = errors.New("pinned by commit")

// pluginFiles are the configured plugins a command runs, each by name: dir holds them all, as a
// bootstrap's tools/plugins does, and files and sums name each one's binary and SHA256SUMS.
type pluginFiles struct {
	dir   string
	files map[string]string
	sums  map[string]string
}

// pluginsFor finds the plugins need picks as the bootstrap would: dir's copy when it is its pin, else the
// pinned release; one off its pin runs if an sbom or correlator, never a driver. gathered: linked apart.
func pluginsFor(ctx context.Context, cfg *config.Config, dir, platform string, need func(config.Plugin) bool, gathered bool) (*pluginFiles, error) {
	host := platform == config.Platform()
	out := &pluginFiles{dir: dir, files: map[string]string{}, sums: map[string]string{}}
	fetched := map[string]fetch.Got{}

	for _, p := range cfg.Plugins {
		if !need(p) {
			continue
		}

		local := filepath.Join(dir, p.Name)

		if host && usableAsIs(p, dir) {
			out.files[p.Name] = local
			if _, err := os.Stat(local + sumsSuffix); err == nil {
				out.sums[p.Name] = local + sumsSuffix
			}

			continue
		}

		if p.Release == nil {
			exe, err := builtPlugin(ctx, p, dir, platform)
			if err != nil {
				return nil, err
			}

			if sum, err := fileSHA256(local); err == nil && host && sum != fetch.FileSum(exe) {
				warnf(os.Stderr, "pipelinegen: %s is not the build plugin %s's pin names; the pinned one runs: %s\n", local, p.Name, exe)
			}

			fetched[p.Name] = fetch.Got{Path: exe}
			out.files[p.Name] = exe

			continue
		}

		got, err := fetchPlugin(ctx, p, platform)
		if err != nil {
			return nil, err
		}

		if _, err := os.Stat(local); err == nil && host {
			warnf(os.Stderr, "pipelinegen: %s is not the build plugin %s's pin names (%s); the pinned release runs: %s\n",
				local, p.Name, p.Release.Version, got.Path)
		}

		fetched[p.Name] = got
		out.files[p.Name] = got.Path
	}

	if len(fetched) == 0 && !gathered {
		return out, nil
	}

	return out, out.gather(platform, fetched)
}

const sumsSuffix = ".SHA256SUMS"

// usableAsIs is dir's copy when it runs as before: the build the pin names, or, but for a driver, any
// build, as a plugin under development is. A zip names its build, which is always the one that runs.
func usableAsIs(p config.Plugin, dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, p.Name)); err != nil || p.Zip != nil {
		return false
	}

	if p.Kind != config.KindDriver || p.Release == nil {
		return true
	}

	_, err := pinnedBinary(p, dir)

	return err == nil
}

func commitPinned(p config.Plugin, dir, platform string) error {
	prog := p.Program()
	build := fmt.Sprintf("git clone %s %s && git -C %s checkout %s && (cd %s && CGO_ENABLED=0 go build -trimpath -o %s ./cmd/%s)",
		config.CloneURL(p.Repository), prog, prog, p.Commit, path.Join(prog, p.Path), filepath.Join(dir, p.Name), prog)

	if platform != config.Platform() {
		return fmt.Errorf("%w: plugin %s is pinned by commit %s, which has no release to download for %s: "+
			"run on this machine with --no-image and a build of it in %s: %s", errCommitPin, p.Name, short12(p.Commit), platform, dir, build)
	}

	return fmt.Errorf("%w: plugin %s is pinned by commit %s, which has no release to download, and %s has no build of it: build it there: %s",
		errCommitPin, p.Name, short12(p.Commit), dir, build)
}

// envReleasesURL replaces where the plugins' releases are fetched from, for tests that serve their
// own; what is fetched is still checked by its pin.
const envReleasesURL = "PIPELINEGEN_TEST_RELEASES_URL"

func fetchPlugin(ctx context.Context, p config.Plugin, platform string) (fetch.Got, error) {
	rp := pin.Pin{
		Name: p.Name, Repository: p.Repository, Path: p.Path, Version: p.Release.Version, Program: p.Program(),
		Sums: p.Release.Sums, URL: os.Getenv(envReleasesURL),
	}
	if p.Release.SHA256 != "" {
		// a sha256 pin is the linux-amd64 file's checksum, or a Node plugin's one script's
		rp.SHA256 = pin.Checksums{config.LinuxAMD64: p.Release.SHA256}
		rp.Script = p.Release.SHA256
	}

	got, err := releases().Fetch(ctx, rp, platform)
	if err != nil {
		return got, fmt.Errorf("plugin %s %s for %s: %w", p.Name, p.Release.Version, platform, err)
	}

	return got, nil
}

// releasesKept is how long a release file fetched into the user cache stays there unused.
const releasesKept = 30 * 24 * time.Hour

const releasesDir = "releases"

var trimReleasesOnce sync.Once

// releases is the fetcher of the plugins' and pipelinegen's own releases. A command's first fetch
// first removes what no command used for releasesKept, so the cache doesn't grow without bound.
func releases() fetch.Fetcher {
	trimReleasesOnce.Do(func() {
		dir := filepath.Join(defaultCache(), releasesDir)

		removed, err := store.Trim(dir, releasesKept, time.Now())
		if err != nil {
			warnf(os.Stderr, "pipelinegen: %v\n", err)
		}

		if len(removed) > 0 {
			warnf(os.Stderr, "pipelinegen: removed %d release files no command used in %d days from %s\n", len(removed), releasesKept/(24*time.Hour), dir)
		}
	})

	return fetch.Fetcher{Client: &http.Client{Timeout: 5 * time.Minute}, Cache: defaultCache(), Under: releasesDir}
}

// gather puts every plugin in one directory of the cache, by name, with each SHA256SUMS beside its
// plugin: the layout the plugins' callers read, which check each binary against its pin again.
func (f *pluginFiles) gather(platform string, fetched map[string]fetch.Got) error {
	names := make([]string, 0, len(f.files))
	for name := range f.files {
		names = append(names, name)
	}

	slices.Sort(names)

	key := sha256.New()
	for _, name := range names {
		key.Write([]byte(name + "\x00" + f.files[name] + "\x00"))
	}

	dir := filepath.Join(defaultCache(), "plugin-sets", platform, hex.EncodeToString(key.Sum(nil))[:16])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}

	for _, name := range names {
		target, err := filepath.EvalSymlinks(f.files[name])
		if err == nil {
			target, err = filepath.Abs(target)
		}

		if err != nil {
			return fmt.Errorf("plugins: %w", err)
		}

		f.files[name] = target

		if err := replaceLink(target, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("plugins: %w", err)
		}

		data, err := sumsData(f.sums[name], fetched, name)
		if err != nil {
			return err
		}

		sums := ""
		if data != nil {
			sums = filepath.Join(dir, name+sumsSuffix)
			err = writeAtomically(sums, data)
		} else {
			err = os.Remove(filepath.Join(dir, name+sumsSuffix))
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}

		if err != nil {
			return fmt.Errorf("plugins: %w", err)
		}

		f.sums[name] = sums
	}

	f.dir = dir

	return nil
}

// sumsData is the SHA256SUMS a plugin's pin checks it by: its release's, or the one beside dir's copy.
func sumsData(local string, fetched map[string]fetch.Got, name string) ([]byte, error) {
	if got, ok := fetched[name]; ok {
		return got.Sums, nil
	}

	if local == "" {
		return nil, nil
	}

	data, err := os.ReadFile(local) //nolint:gosec // the release's list beside its plugin
	if err != nil {
		return nil, fmt.Errorf("plugins: %w", err)
	}

	return data, nil
}

func writeAtomically(p string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.new", p, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}

	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // the rename's error says it

		return err //nolint:wrapcheck // the caller says whose
	}

	return nil
}

// planningPlugins are those planning asks: the sboms and correlators.
func planningPlugins(p config.Plugin) bool {
	return p.Kind == config.KindSBOM || p.Kind == config.KindCorrelator
}

// suitePlugins are those a batch of the suite runs: its drivers, and its correlators when it records.
func suitePlugins(cfg *config.Config, suite string, records bool) func(config.Plugin) bool {
	s := cfg.Suites[suite]

	return func(p config.Plugin) bool {
		switch p.Kind {
		case config.KindDriver:
			return slices.ContainsFunc(s.Steps, func(st config.Step) bool { return st.Driver() == p.Name })
		case config.KindCorrelator:
			return records && slices.Contains(cfg.CorrelatorsFor(suite), p.Name)
		}

		return false
	}
}

// claimsWithPlugins finds the planning plugins, when the plan asks any, and then the sboms' claims.
func claimsWithPlugins(ctx context.Context, cfg *config.Config, cache, dir, root, head, known string) (map[string]string, string, error) {
	if known != "" {
		return nil, dir, nil
	}

	dir, err := planningPluginsDir(ctx, cfg, dir, root)
	if err != nil {
		return nil, dir, err
	}

	return claimsIfUnknown(ctx, cfg, cache, dir, root, head, known), dir, nil
}

// planningPluginsDir is pluginsFor on this machine, for planning. When the plan asks no plugin but an
// sbom's claims, which are best effort, a plugin it can't find or fetch is left out as before.
func planningPluginsDir(ctx context.Context, cfg *config.Config, dir, root string) (string, error) {
	f, err := pluginsFor(ctx, cfg, dir, config.Platform(), planningPlugins, false)

	switch {
	case err == nil:
		return f.dir, nil
	case loadsPlugins(cfg, hasDepsRepo(filepath.Join(root, cfg.Bazel.Workspace))):
		return dir, err
	default:
		return dir, nil
	}
}

func platformOf(osArch string) string { return strings.ReplaceAll(osArch, "/", "-") }
