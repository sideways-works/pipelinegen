package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/forge"
	"github.com/sideways-works/pipelinegen/internal/pinedit"
	"github.com/sideways-works/plugkit/fetch"
	"github.com/sideways-works/plugkit/semver"
)

var errPin = errors.New("can't pin")

// The keys of a plugin's entry that every pin writes.
const (
	keyName = "name"
	keyKind = "kind"
)

// parseMixed parses flags that may come before, between and after the positional arguments.
func parseMixed(ctx context.Context, fs *flag.FlagSet, args []string) ([]string, error) {
	if _, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet); ok {
		return nil, parse(ctx, fs, nil)
	}

	fs.SetOutput(io.Discard)

	var pos []string

	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, flag.ErrHelp
			}

			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}

		if fs.NArg() == 0 {
			break
		}

		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}

	return pos, absolutePaths(fs)
}

// pinFile is the config a pin command edits, read and checked before any edit.
func pinFile(c common) (string, []byte, *config.Config, error) {
	file := c.config
	if file == "" {
		file = filepath.Join(absRoot(c.root), config.FileName)
	}

	data, err := os.ReadFile(file) //nolint:gosec // the config the caller names
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %w", errPin, err)
	}

	cfg, err := config.Parse(data)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %s must read before it is edited: %w", errPin, file, err)
	}

	return file, data, cfg, nil
}

// writePins writes the edited config in place of file once it reads as a config, keeping the
// file's mode.
func writePins(file string, data []byte) error {
	if _, err := config.Parse(data); err != nil {
		return fmt.Errorf("%w: the edited config doesn't read, so %s is left as it was: %w", errPin, file, err)
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
	}

	tmp := fmt.Sprintf("%s.%d.new", file, os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("%w: %w", errPin, err)
	}

	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // the rename's error says it

		return fmt.Errorf("%w: %w", errPin, err)
	}

	return nil
}

func pinChecker() (forge.Checker, func(), error) {
	work, err := os.MkdirTemp("", "pipelinegen-pin-")
	if err != nil {
		return forge.Checker{}, nil, fmt.Errorf("%w: %w", errPin, err)
	}

	client := forge.Client()

	return forge.Checker{Client: client, Fetcher: fetch.Fetcher{Client: client, Cache: defaultCache(), Under: releasesDir}, Work: work},
		func() { _ = os.RemoveAll(work) }, nil //nolint:errcheck // a scratch directory
}

// pinRelease is a release as a new entry writes it.
type pinRelease struct {
	Version string `yaml:"version"`
	Sums    string `yaml:"sums"`
}

func pluginAdd(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c                          common
		version, name, zip, zipSum string
	)

	fs := flag.NewFlagSet("plugin add", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &version, "version", "", "the release to pin, vX.Y.Z, the same for every path (default: the newest that is no prerelease)")
	textVar(fs, &name, "name", "", "the entry's name, with one path or a zip (default: the name the plugin's describe answer or manifest gives)")
	textVar(fs, &zip, "zip", "", "pin the plugin zip at this https URL instead of a release, by its sha256, named and kinded by its manifest.yaml")
	textVar(fs, &zipSum, "sha256", "", "with --zip: the sha256 the zip must have (default: whatever the download has)")

	pos, err := parseMixed(ctx, fs, args)
	if err != nil {
		return err
	}

	if zip != "" || zipSum != "" {
		return pluginAddZip(ctx, c, pos, version, zip, zipSum, name, stdout)
	}

	if len(pos) == 0 {
		return fmt.Errorf("%w: plugin add <git-url> [<path>...], or plugin add --zip <url>", errUsage)
	}

	repository, paths := pos[0], pos[1:]
	if len(paths) == 0 {
		paths = []string{""}
	}

	if name != "" && len(paths) > 1 {
		return fmt.Errorf("%w: --name names one plugin, and %d paths are given", errUsage, len(paths))
	}

	file, data, cfg, err := pinFile(c)
	if err != nil {
		return err
	}

	doc, err := pinedit.Parse(data)
	if err != nil {
		return err
	}

	k, done, err := pinChecker()
	if err != nil {
		return err
	}
	defer done()

	var lines []string

	for _, dir := range paths {
		line, err := addOne(ctx, k, cfg, doc, repository, dir, version, name)
		if err != nil {
			return err
		}

		lines = append(lines, line)
	}

	if err := writePins(file, doc.Bytes()); err != nil {
		return err
	}

	return say(stdout, "%s", strings.Join(lines, ""))
}

func pluginAddZip(ctx context.Context, c common, pos []string, version, zip, zipSum, name string, stdout io.Writer) error {
	switch {
	case zip == "":
		return fmt.Errorf("%w: --sha256 checks the zip --zip names", errUsage)
	case len(pos) > 0 || version != "":
		return fmt.Errorf("%w: plugin add --zip <url> takes no repository, path or --version: the zip's manifest names the plugin", errUsage)
	}

	if u, err := url.Parse(zip); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: --zip %q: an https URL", errUsage, zip)
	}

	file, data, cfg, err := pinFile(c)
	if err != nil {
		return err
	}

	doc, err := pinedit.Parse(data)
	if err != nil {
		return err
	}

	line, err := addZip(ctx, cfg, doc, zip, zipSum, name)
	if err != nil {
		return err
	}

	if err := writePins(file, doc.Bytes()); err != nil {
		return err
	}

	return say(stdout, "%s", line)
}

func addOne(ctx context.Context, k forge.Checker, cfg *config.Config, doc *pinedit.Doc, repository, dir, version, name string) (string, error) {
	if err := config.CheckSource(repository, dir); err != nil {
		return "", fmt.Errorf("%w: %w", errUsage, err)
	}

	r, err := chooseRelease(ctx, k, repository, dir, version, false)
	if err != nil {
		return "", err
	}

	program := config.ProgramOf(repository, dir)

	checked, err := k.Check(ctx, repository, dir, program, r)
	if err != nil {
		return "", fmt.Errorf("%w %s: %w", errPin, where(repository, dir), err)
	}

	if name == "" {
		name = checked.Name
	}

	if slices.ContainsFunc(cfg.Plugins, func(p config.Plugin) bool { return p.Name == name }) {
		return "", fmt.Errorf("%w %s: the config has a plugin named %s; `pipelinegen plugin upgrade %s` moves its pin, --name gives this one another name", errPin, where(repository, dir), name, name)
	}

	if _, ok := doc.Find(name); ok {
		return "", fmt.Errorf("%w %s: two paths give the name %s; add them one at a time with --name", errPin, where(repository, dir), name)
	}

	rel := pinRelease{Version: checked.Version, Sums: checked.Sums}

	if dir == "" {
		err = doc.AddFlat([]pinedit.Field{{Key: keyName, Value: name}, {Key: keyKind, Value: checked.Kind}, {Key: "repository", Value: repository}, {Key: "release", Value: rel}})
	} else {
		err = doc.AddToGroup(repository, []pinedit.Field{{Key: keyName, Value: name}, {Key: keyKind, Value: checked.Kind}, {Key: "path", Value: dir}, {Key: "release", Value: rel}})
	}

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("added %s (%s) from %s: %s, %d files checked\n", name, checked.Kind, where(repository, dir), checked.Version, len(checked.Files)), nil
}

func where(repository, dir string) string {
	if dir == "" {
		return repository
	}

	return repository + " at " + dir
}

// chooseRelease is the release of version, or the newest one, a prerelease only with pre.
func chooseRelease(ctx context.Context, k forge.Checker, repository, dir, version string, pre bool) (forge.Release, error) {
	rs, err := forge.Releases(ctx, k.Client, repository, dir)
	if err != nil {
		return forge.Release{}, fmt.Errorf("%w: %w", errPin, err)
	}

	tag := "vX.Y.Z"
	if dir != "" {
		tag = dir + "/" + tag
	}

	if version != "" {
		want, err := semver.Parse(version)
		if err != nil || !strings.HasPrefix(version, "v") {
			return forge.Release{}, fmt.Errorf("%w: --version %q: a version vX.Y.Z", errUsage, version)
		}

		for _, r := range rs {
			if r.Version.Compare(want) == 0 {
				return r, nil
			}
		}

		return forge.Release{}, fmt.Errorf("%w %s: no release %s (tagged %s)", errPin, where(repository, dir), version, tag)
	}

	r, ok := forge.Newest(rs, pre)
	if !ok {
		return forge.Release{}, fmt.Errorf("%w %s: no release tagged %s%s", errPin, where(repository, dir), tag, map[bool]string{false: " that is no prerelease", true: ""}[pre])
	}

	return r, nil
}

func pluginUpgrade(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c   common
		pre bool
	)

	fs := flag.NewFlagSet("plugin upgrade", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&pre, "pre", false, "move to a prerelease too, when it is the newest release")

	names, err := parseMixed(ctx, fs, args)
	if err != nil {
		return err
	}

	file, data, cfg, err := pinFile(c)
	if err != nil {
		return err
	}

	targets, err := upgradeTargets(cfg, names)
	if err != nil {
		return err
	}

	doc, err := pinedit.Parse(data)
	if err != nil {
		return err
	}

	k, done, err := pinChecker()
	if err != nil {
		return err
	}
	defer done()

	var (
		lines []string
		moved bool
	)

	var failed []string

	// a plugin that can't be moved is said and left as it was; the others still move
	for _, p := range targets {
		line, changed, err := upgradeOne(ctx, k, doc, p, pre)
		if err != nil {
			failed, line = append(failed, p.Name), fmt.Sprintf("%s: not moved: %v\n", p.Name, err)
		}

		lines, moved = append(lines, line), moved || changed
	}

	if moved {
		if err := writePins(file, doc.Bytes()); err != nil {
			return err
		}
	}

	if err := say(stdout, "%s", strings.Join(lines, "")); err != nil || len(failed) == 0 {
		return err
	}

	return fmt.Errorf("%w: %d of %d plugins not moved: %s", errPin, len(failed), len(targets), strings.Join(failed, ", "))
}

// upgradeTargets are the named plugins, or every plugin pinned by a release.
func upgradeTargets(cfg *config.Config, names []string) ([]config.Plugin, error) {
	if len(names) == 0 {
		var out []config.Plugin

		for _, p := range cfg.Plugins {
			if p.Release != nil {
				out = append(out, p)
			}
		}

		return out, nil
	}

	out := make([]config.Plugin, 0, len(names))

	for _, name := range names {
		i := slices.IndexFunc(cfg.Plugins, func(p config.Plugin) bool { return p.Name == name })

		switch {
		case i < 0:
			return nil, fmt.Errorf("%w: the config has no plugin %s", errUsage, name)
		case cfg.Plugins[i].Zip != nil:
			return nil, fmt.Errorf("%w: plugin %s is pinned by a zip, which has no newer release to move to; give its zip pin the newer zip's url and sha256", errPin, name)
		case cfg.Plugins[i].Release == nil:
			return nil, fmt.Errorf("%w: plugin %s is pinned by commit; pin a release with `pipelinegen plugin add` in its place", errPin, name)
		}

		out = append(out, cfg.Plugins[i])
	}

	return out, nil
}

func upgradeOne(ctx context.Context, k forge.Checker, doc *pinedit.Doc, p config.Plugin, pre bool) (string, bool, error) {
	r, err := chooseRelease(ctx, k, p.Repository, p.Path, "", pre)
	if err != nil {
		return "", false, err
	}

	if cur, err := semver.Parse(p.Release.Version); err == nil && r.Version.Compare(cur) <= 0 {
		return fmt.Sprintf("%s: %s, the newest\n", p.Name, p.Release.Version), false, nil
	}

	checked, err := k.Check(ctx, p.Repository, p.Path, p.Program(), r)
	if err != nil {
		return "", false, fmt.Errorf("%w %s: %w", errPin, p.Name, err)
	}

	if checked.Kind != p.Kind {
		return "", false, fmt.Errorf("%w %s: release %s is a %s, and the entry's kind is %s", errPin, p.Name, r.Tag, checked.Kind, p.Kind)
	}

	key, sum := "sums", checked.Sums
	if p.Release.Sums == "" {
		key, sum = "sha256", checked.SHA256(p.Program())
	}

	if sum == "" {
		return "", false, fmt.Errorf("%w %s: release %s has no linux-amd64 file or script for a sha256 pin; pin it by sums", errPin, p.Name, r.Tag)
	}

	if err := doc.SetRelease(p.Name, checked.Version, key, sum); err != nil {
		return "", false, err
	}

	return fmt.Sprintf("%s: %s -> %s\n", p.Name, p.Release.Version, checked.Version), true, nil
}
