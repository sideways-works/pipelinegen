package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/pinedit"
	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/build"
	"github.com/sideways-works/plugkit/bundle"
	"github.com/sideways-works/plugkit/manifest"
	"github.com/sideways-works/plugkit/pin"
	"github.com/sideways-works/plugkit/semver"
	"github.com/sideways-works/plugkit/store"
)

var errBuilt = errors.New("plugin")

// pluginStore resolves the plugins pinned by a commit or a zip: under cache's plugins directory,
// where its size budget counts and trims them, or with no cache into the user cache, beside the
// releases, where the same trimming keeps them.
func pluginStore(cache string) store.Store {
	client := &http.Client{Timeout: 10 * time.Minute}
	if cache != "" {
		return store.Store{Dir: filepath.Join(cache, "plugins"), Client: client}
	}

	f := releases()

	return store.Store{Dir: filepath.Join(f.Cache, f.Under), Client: client}
}

// pluginCache is the cache a command's --cache names for the plugins it builds or unpacks: none when
// it is the user cache or in it, as the default is, so those stay beside the releases.
func pluginCache(cache string) string {
	if cache == "" {
		return ""
	}

	abs := absOr(cache)
	if rel, err := filepath.Rel(defaultCache(), abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}

	return abs
}

// resolveBuilt is the plugin p's commit or zip pins, for platform (linux-amd64): built from its
// manifest, or unpacked from its zip, into cache (see pluginStore), and checked to be a program of
// the entry's kind.
func resolveBuilt(ctx context.Context, p config.Plugin, platform, cache string) (store.Resolved, error) {
	sp := store.Pin{Path: p.Path, Commit: p.Commit}
	if p.Repository != "" {
		sp.Repository = config.CloneURL(p.Repository)
	}

	if p.Zip != nil {
		sp.Zip = &store.Zip{URL: p.Zip.URL, SHA256: p.Zip.SHA256}
	}

	osArch := strings.Replace(platform, "-", "/", 1)

	s := pluginStore(cache)
	s.OnBuild = func(string) {
		warnf(os.Stderr, "pipelinegen: building plugin %s for %s from its commit %s\n", p.Name, osArch, short12(p.Commit))
	}

	r, err := s.Resolve(ctx, sp, osArch)
	if err != nil {
		return r, fmt.Errorf("plugin %s: %w", p.Name, err)
	}

	switch {
	case r.Manifest.Kind != p.Kind:
		return r, fmt.Errorf("%w %s: its manifest says it is a %s, and the entry's kind is %s", errBuilt, p.Name, r.Manifest.Kind, p.Kind)
	case r.Program == "":
		return r, fmt.Errorf("%w %s: it holds assets alone, and pipelinegen runs a %s plugin as a program", errBuilt, p.Name, p.Kind)
	}

	return r, nil
}

// builtPlugin is the program a plugin pinned by commit or zip runs: a commit whose tree holds no
// manifest, or that can't be fetched, is refused as before, saying how to build it by hand.
func builtPlugin(ctx context.Context, p config.Plugin, dir, platform, cache string) (string, error) {
	r, err := resolveBuilt(ctx, p, platform, cache)

	switch {
	case err == nil:
		return r.Program, nil
	case p.Commit != "" && (errors.Is(err, store.ErrNoManifest) || errors.Is(err, store.ErrPin)):
		return "", fmt.Errorf("%w (building it from its %s: %w)", commitPinned(p, dir, platform), manifest.FileName, err)
	}

	return "", err
}

func pluginBuild(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		out, tool, version string
		platforms          []string
	)

	fs := flag.NewFlagSet("plugin build", flag.ContinueOnError)
	pathVar(fs, &out, "zip", "", "the zip to write, with SHA256SUMS beside it (required)", writesFile)
	textVar(fs, &tool, "tool", "", "the container tool the build runs in, docker or podman (default: the first on PATH)")
	textVar(fs, &version, "version", "", "the version the build stands for, vX.Y.Z: the manifest's {version} (default none)")
	fs.Func("platform", "build for this os/arch (repeatable; default every platform of the manifest)", func(s string) error {
		platforms = append(platforms, s)

		return nil
	})

	pos, err := parseMixed(ctx, fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 || out == "" {
		return fmt.Errorf("%w: plugin build <dir> --zip <out.zip>, with --platform os/arch for each platform to build", errUsage)
	}

	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return fmt.Errorf("%w: %w", errBuilt, err)
	}

	data, err := os.ReadFile(filepath.Join(dir, manifest.FileName)) //nolint:gosec // the plugin's manifest the caller names
	if err != nil {
		return fmt.Errorf("%w: %w", errBuilt, err)
	}

	root, rel := repositoryOf(ctx, dir)

	work, err := os.MkdirTemp("", "pipelinegen-plugin-build-")
	if err != nil {
		return fmt.Errorf("%w: %w", errBuilt, err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	laid := filepath.Join(work, "plugin")

	commit, version, err := stampOf(ctx, root, version)
	if err != nil {
		return err
	}

	m, err := build.Build(ctx, build.Options{
		Tool: tool, Root: root, Dir: rel, Manifest: data, Platforms: platforms, Out: laid, Log: os.Stderr,
		Commit: commit, Version: version,
	})
	if err != nil {
		return err
	}

	var z bytes.Buffer
	if err := bundle.Pack(laid, &z); err != nil {
		return err
	}

	sum, sums, err := writeBuiltZip(out, z.Bytes())
	if err != nil {
		return err
	}

	return say(stdout, "built %s (%s) into %s, sha256 %s; %s lists it\npin it with: zip: {url: <where you host %s>, sha256: %s}\n",
		m.Name, m.Kind, out, sum, sums, filepath.Base(out), sum)
}

// writeBuiltZip writes the zip to out and lists it in the SHA256SUMS beside it, returning its sha256 and
// the list's path.
func writeBuiltZip(out string, data []byte) (string, string, error) {
	sum := sumOfBytes(data)

	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return "", "", fmt.Errorf("%w: %w", errBuilt, err)
	}

	if err := writeAtomically(out, data); err != nil {
		return "", "", fmt.Errorf("%w: %w", errBuilt, err)
	}

	sums := filepath.Join(filepath.Dir(out), "SHA256SUMS")
	if err := writeAtomically(sums, listedWith(sums, filepath.Base(out), sum)); err != nil {
		return "", "", fmt.Errorf("%w: %w", errBuilt, err)
	}

	return sum, sums, nil
}

// listedWith is the SHA256SUMS at file with name's line set to sum, the other zips' lines kept, so
// several plugins built into one directory are each listed.
func listedWith(file, name, sum string) []byte {
	old, _ := os.ReadFile(file) //nolint:errcheck,gosec // none yet is an empty list; the list beside the zip the caller names

	var b strings.Builder

	for line := range strings.Lines(string(old)) {
		if _, listed, ok := strings.Cut(strings.TrimSpace(line), "  "); ok && strings.TrimPrefix(listed, "*") != name {
			b.WriteString(strings.TrimRight(line, "\n") + "\n")
		}
	}

	b.WriteString(sum + "  " + name + "\n")

	return []byte(b.String())
}

// stampOf is what a build's {commit} and {version} stand for: the commit checked out at root, and
// --version without its v.
func stampOf(ctx context.Context, root, version string) (string, string, error) {
	if version != "" {
		if _, err := semver.Parse(version); err != nil {
			return "", "", fmt.Errorf("%w: --version %q: a version vX.Y.Z", errUsage, version)
		}
	}

	return builtCommit(ctx, root), strings.TrimPrefix(version, "v"), nil
}

// builtCommit is the commit checked out in the repository at root, the manifest's {commit}: empty
// outside one. A tree with changes is built as it is, and said so.
func builtCommit(ctx context.Context, root string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output() //nolint:gosec // git, asked about the directory the caller names
	if err != nil {
		return ""
	}

	commit := strings.TrimSpace(string(out))

	if changed, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain").Output(); err == nil && len(changed) > 0 { //nolint:gosec // as above
		warnf(os.Stderr, "pipelinegen: %s has changes that aren't committed: the build is of the tree as it is, stamped with %s\n", root, short12(commit))
	}

	return commit
}

// repositoryOf is the git repository dir is in and dir's path in it, or dir itself when it is in
// none: the build sees the whole repository, as a plugin's module may sit above its directory.
func repositoryOf(ctx context.Context, dir string) (string, string) {
	top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output() //nolint:gosec // git, asked where the directory the caller names is
	if err != nil {
		return dir, ""
	}

	root := strings.TrimSpace(string(top))

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir, ""
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return dir, ""
	}

	if rel == "." {
		rel = ""
	}

	return root, filepath.ToSlash(rel)
}

func sumOfBytes(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// addZip pins the zip at u: downloaded, checked against want when given, unpacked as a resolve would
// unpack it, and named and kinded by its manifest.
func addZip(ctx context.Context, cfg *config.Config, doc *pinedit.Doc, u, want, name string) (string, error) {
	work, err := os.MkdirTemp("", "pipelinegen-pin-zip-")
	if err != nil {
		return "", fmt.Errorf("%w: %w", errPin, err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	file := filepath.Join(work, "plugin.zip")
	if err := download(ctx, u, file); err != nil {
		return "", err
	}

	sum, err := pin.FileSHA256(file)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errPin, err)
	}

	if want != "" && strings.ToLower(want) != sum {
		return "", fmt.Errorf("%w %s: its sha256 is %s, and --sha256 says %s", errPin, u, sum, want)
	}

	m, err := bundle.Unpack(file, filepath.Join(work, "plugin"), bundle.Limits{})
	if err != nil {
		return "", fmt.Errorf("%w %s: %w", errPin, u, err)
	}

	switch {
	case !slices.Contains(plugin.Kinds, m.Kind):
		return "", fmt.Errorf("%w %s: its manifest's kind is %s, which pipelinegen doesn't run: %s", errPin, u, m.Kind, strings.Join(plugin.Kinds, ", "))
	case m.Program == "":
		return "", fmt.Errorf("%w %s: it holds assets alone, and pipelinegen runs a %s plugin as a program", errPin, u, m.Kind)
	}

	if name == "" {
		name = m.Name
	}

	if slices.ContainsFunc(cfg.Plugins, func(p config.Plugin) bool { return p.Name == name }) {
		return "", fmt.Errorf("%w %s: the config has a plugin named %s; --name gives this one another name", errPin, u, name)
	}

	if err := doc.AddFlat([]pinedit.Field{{Key: keyName, Value: name}, {Key: keyKind, Value: m.Kind}, {Key: "zip", Value: config.Zip{URL: u, SHA256: sum}}}); err != nil {
		return "", err
	}

	return fmt.Sprintf("added %s (%s) from %s: sha256 %s, built for %s\n", name, m.Kind, u, sum, strings.Join(m.Platforms, ", ")), nil
}

// maxZip bounds a plugin's zip, so a wrong URL can't fill the disk.
const maxZip = 1 << 30

func download(ctx context.Context, u, file string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errPin, err)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errPin, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w %s: %s", errPin, u, resp.Status)
	}

	f, err := os.Create(file) //nolint:gosec // the command's own scratch file
	if err != nil {
		return fmt.Errorf("%w: %w", errPin, err)
	}

	n, err := io.Copy(f, io.LimitReader(resp.Body, maxZip+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	switch {
	case err != nil:
		return fmt.Errorf("%w %s: %w", errPin, u, err)
	case n > maxZip:
		return fmt.Errorf("%w %s: over %d bytes", errPin, u, maxZip)
	}

	return nil
}

// maxManifest bounds a manifest read from a zip.
const maxManifest = 1 << 20

func pluginDescribe(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin describe", flag.ContinueOnError)

	pos, err := parseMixed(ctx, fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return fmt.Errorf("%w: plugin describe <dir|zip>", errUsage)
	}

	data, err := manifestOf(pos[0])
	if err != nil {
		return err
	}

	m, err := manifest.Parse(data)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errBuilt, pos[0], err)
	}

	return say(stdout, "%s", describeManifest(m))
}

// manifestOf is the manifest.yaml of a plugin's directory, or at the top level of its zip.
func manifestOf(p string) ([]byte, error) {
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		data, err := os.ReadFile(filepath.Join(p, manifest.FileName)) //nolint:gosec // the plugin's manifest the caller names
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBuilt, err)
		}

		return data, nil
	}

	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is neither a plugin's directory nor its zip: %w", errBuilt, p, err)
	}
	defer zr.Close() //nolint:errcheck // read only

	f, err := zr.Open(manifest.FileName)
	if err != nil {
		return nil, fmt.Errorf("%w: %s has no %s at its top level", errBuilt, p, manifest.FileName)
	}
	defer f.Close() //nolint:errcheck // read only

	data, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil || len(data) > maxManifest {
		return nil, fmt.Errorf("%w: %s's %s is unreadable or over %d bytes", errBuilt, p, manifest.FileName, maxManifest)
	}

	return data, nil
}

func describeManifest(m manifest.Manifest) string {
	var b strings.Builder

	line := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-12s %s\n", key+":", value)
		}
	}

	runtime := "native"
	if m.Program == "" {
		runtime = ""
	}

	if n, r := m.RuntimeOf(); n != "" {
		runtime = strings.TrimSpace(n + " " + r)
	}

	line("name", m.Name)
	line("kind", m.Kind)
	line("program", m.Program)
	line("runtime", runtime)
	line("platforms", strings.Join(m.Platforms, ", "))
	line("description", m.Description)
	line("implements", strings.Join(m.Implements, ", "))
	line("requires", strings.Join(m.Requires, ", "))

	for _, p := range m.Permissions {
		line("permission", fmt.Sprintf("%s (from %s): %s", p.Name, p.Default, p.Description))
	}

	return b.String()
}
