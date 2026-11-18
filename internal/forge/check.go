package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/fetch"
	"github.com/sideways-works/plugkit/pin"
)

const SumsFile = "SHA256SUMS"

var ErrRelease = errors.New("release can't be pinned")

// Checked is a release each of whose program's files was downloaded and matched its SHA256SUMS,
// with the name and kind the plugin's describe answer gives.
type Checked struct {
	Release Release
	// Version is the release's version without the path, as a pin writes it.
	Version string
	// Sums is the SHA-256 of the release's SHA256SUMS.
	Sums string
	// Files are the program's files the release holds, by name, with their SHA-256.
	Files map[string]string
	Name  string
	Kind  string
}

// SHA256 is the checksum a pin by one sha256 gives: the linux-amd64 file's, or a Node plugin's
// one script's.
func (c Checked) SHA256(program string) string {
	for _, f := range []string{program + "-linux-amd64", program + ".mjs"} {
		if sum, ok := c.Files[f]; ok {
			return sum
		}
	}

	return ""
}

// Checker downloads what a pin of a release names, through Fetcher, which keeps each file in its
// cache by its checksum.
type Checker struct {
	Client  *http.Client
	Fetcher fetch.Fetcher
	// Work is where a plugin is asked to describe itself when its release holds no plugin.json.
	Work string
}

var platformFile = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

// Check downloads the release's SHA256SUMS and every file of the program it lists, each checked
// by its line, and reads the plugin's describe answer.
func (k Checker) Check(ctx context.Context, repository, dir, program string, r Release) (Checked, error) {
	version := strings.TrimPrefix(r.Tag, dir+"/")
	if dir == "" {
		version = r.Tag
	}

	sums, err := Download(ctx, k.Client, r, SumsFile)
	if err != nil {
		return Checked{}, fmt.Errorf("%w: %w: a pin is checked by its %s", ErrRelease, err, SumsFile)
	}

	c := Checked{Release: r, Version: version, Sums: hexSum(sums), Files: map[string]string{}}
	listed := pin.Listed(sums)

	var platforms []string

	for file, sum := range listed {
		switch rest, ok := strings.CutPrefix(file, program+"-"); {
		case file == program+".mjs":
			platforms = append(platforms, pin.Platform())
		case ok && platformFile.MatchString(rest):
			platforms = append(platforms, rest)
		default:
			continue
		}

		c.Files[file] = sum
	}

	if len(c.Files) == 0 {
		return Checked{}, fmt.Errorf("%w: release %s: its %s lists no file of %s (%s-<os>-<arch> or %s.mjs)", ErrRelease, r.Tag, SumsFile, program, program, program)
	}

	slices.Sort(platforms)

	p := pin.Pin{Name: program, Repository: repository, Path: dir, Version: version, Program: program, Sums: c.Sums}

	got := map[string]string{}

	for _, platform := range slices.Compact(platforms) {
		g, err := k.Fetcher.Fetch(ctx, p, platform)
		if err != nil {
			return Checked{}, fmt.Errorf("%w: release %s: %w", ErrRelease, r.Tag, err)
		}

		got[platform] = g.Path
	}

	c.Name, c.Kind, err = k.describe(ctx, r, listed, program, got[pin.Platform()])
	if err != nil {
		return Checked{}, fmt.Errorf("%w: release %s: %w", ErrRelease, r.Tag, err)
	}

	return c, nil
}

// describe reads the release's plugin.json (or <program>.plugin.json) once its checksum is its
// line's, else asks this platform's file.
func (k Checker) describe(ctx context.Context, r Release, listed map[string]string, program, exe string) (string, string, error) {
	var d plugin.Description

	for _, f := range []string{"plugin.json", program + ".plugin.json"} {
		want, ok := listed[f]
		if !ok {
			continue
		}

		data, err := Download(ctx, k.Client, r, f)
		if err != nil {
			return "", "", err
		}

		if sum := hexSum(data); sum != want {
			return "", "", fmt.Errorf("%w: %s is %s, its %s line %s", fetch.ErrChecksum, f, sum, SumsFile, want)
		}

		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", fmt.Errorf("%s: %w", f, err)
		}

		return named(d, f)
	}

	if exe == "" {
		return "", "", fmt.Errorf("%w: no plugin.json, and no file for %s to ask its describe", ErrRelease, pin.Platform())
	}

	d, err := plugin.Plugin{Name: program, Path: exe}.Describe(ctx, k.Work)
	if err != nil {
		return "", "", fmt.Errorf("describe: %w", err)
	}

	return named(d, "its describe")
}

func named(d plugin.Description, from string) (string, string, error) {
	if d.Name == "" || !slices.Contains(plugin.Kinds, d.Kind) {
		return "", "", fmt.Errorf("%w: %s gives no name, or no kind of %s", ErrRelease, from, strings.Join(plugin.Kinds, ", "))
	}

	return d.Name, d.Kind, nil
}

func hexSum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
