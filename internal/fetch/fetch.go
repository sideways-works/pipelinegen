// Package fetch downloads a pinned release's file for a platform into
// pipelinegen's cache and checks it against its pin: a file that doesn't
// match is never kept, so it never runs.
package fetch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrChecksum is a downloaded file whose checksum isn't the pinned one.
var ErrChecksum = errors.New("checksum mismatch")

// ErrPin is a pin no file can be fetched or checked by.
var ErrPin = errors.New("pin")

// DefaultURL is where a release's files are: the repository, the version
// and the file's name fill it in that order.
const DefaultURL = "https://github.com/%s/releases/download/%s/%s"

const sumsFile = "SHA256SUMS"

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Pin is a release and how its files are checked: by a checksum per
// platform, or by the checksum of the release's SHA256SUMS.
type Pin struct {
	// Name is the pin's name in messages: the plugin's or the connector's.
	Name       string
	Repository string
	Version    string
	SHA256     config.Checksums
	// Script is the checksum of the release's <program>.mjs, the one file
	// a Node plugin is released as, the same on every platform.
	Script string
	Sums   string
	// URL overrides DefaultURL.
	URL string
}

// Program is the program the pinned repository holds, which names its
// release's files.
func (p Pin) Program() string { return plugin.Program(p.Repository) }

func (p Pin) binary(platform string) string { return p.Program() + "-" + platform }

func (p Pin) script() string { return p.Program() + ".mjs" }

// Got is a fetched file: its path, its checksum, and the release's
// SHA256SUMS when the pin is by sums, which a plugins directory keeps
// beside the file as the bootstrap does.
type Got struct {
	Path   string
	SHA256 string
	Sums   []byte
}

// Fetcher downloads into Cache, under the directory Under names.
type Fetcher struct {
	Client *http.Client
	Cache  string
	Under  string
}

// Fetch returns the pinned file for platform: the cache's copy when an
// earlier fetch put it there, and otherwise one downloaded and checked
// against the pin.
func (f Fetcher) Fetch(ctx context.Context, pin Pin, platform string) (Got, error) {
	files, sums, err := f.wanted(ctx, pin, platform)
	if err != nil {
		return Got{}, err
	}

	for _, w := range files {
		dest := filepath.Join(f.Cache, f.Under, w.sum, pin.Program())
		if fileSum(dest) == w.sum {
			return Got{Path: dest, SHA256: w.sum, Sums: sums}, nil
		}
	}

	for i, w := range files {
		data, err := f.download(ctx, pin, w.file)
		if errors.Is(err, errNotFound) && i < len(files)-1 {
			continue
		}

		if err != nil {
			return Got{}, err
		}

		if sum := hexSum(data); sum != w.sum {
			return Got{}, fmt.Errorf("%w: %s %s: %s is %s, pinned %s", ErrChecksum, pin.Name, pin.Version, w.file, sum, w.sum)
		}

		dest := filepath.Join(f.Cache, f.Under, w.sum, pin.Program())
		if err := keep(dest, data); err != nil {
			return Got{}, fmt.Errorf("fetch %s: %w", pin.Name, err)
		}

		return Got{Path: dest, SHA256: w.sum, Sums: sums}, nil
	}

	return Got{}, fmt.Errorf("%w: %s %s has nothing for %s", ErrPin, pin.Name, pin.Version, platform)
}

type wantedFile struct{ file, sum string }

// wanted are the files that may hold the pin's program for platform, in
// the order to try them, each with the checksum it must have.
func (f Fetcher) wanted(ctx context.Context, pin Pin, platform string) ([]wantedFile, []byte, error) {
	if pin.Sums != "" {
		sums, err := f.sums(ctx, pin)
		if err != nil {
			return nil, nil, err
		}

		listed := Listed(sums)

		for _, file := range []string{pin.binary(platform), pin.script()} {
			if sum, ok := listed[file]; ok && sha256Hex.MatchString(sum) {
				return []wantedFile{{file, sum}}, sums, nil
			}
		}

		return nil, nil, fmt.Errorf("%w: %s %s: its %s lists no %s or %s", ErrPin, pin.Name, pin.Version, sumsFile, pin.binary(platform), pin.script())
	}

	var out []wantedFile

	if sum, ok := pin.SHA256.For(platform); ok && sha256Hex.MatchString(strings.ToLower(sum)) {
		out = append(out, wantedFile{pin.binary(platform), strings.ToLower(sum)})
	}

	if sha256Hex.MatchString(strings.ToLower(pin.Script)) {
		out = append(out, wantedFile{pin.script(), strings.ToLower(pin.Script)})
	}

	if len(out) == 0 {
		return nil, nil, fmt.Errorf("%w: %s %s has no checksum for %s: a release pinned by one sha256 is checked by its linux-amd64 file's; "+
			"pin it by sums (the checksum of its %s) to fetch it for another platform", ErrPin, pin.Name, pin.Version, platform, sumsFile)
	}

	return out, nil, nil
}

// sums is the release's SHA256SUMS once its checksum is the pin's: the
// cache's copy, kept by that checksum, or a download.
func (f Fetcher) sums(ctx context.Context, pin Pin) ([]byte, error) {
	want := strings.ToLower(pin.Sums)
	if !sha256Hex.MatchString(want) {
		return nil, fmt.Errorf("%w: %s %s: sums %q is no sha256", ErrPin, pin.Name, pin.Version, pin.Sums)
	}

	kept := filepath.Join(f.Cache, f.Under, "sums", want)
	if data, err := os.ReadFile(kept); err == nil && hexSum(data) == want { //nolint:gosec // the cache's own file, checked by its checksum
		return data, nil
	}

	data, err := f.download(ctx, pin, sumsFile)
	if err != nil {
		return nil, err
	}

	if sum := hexSum(data); sum != want {
		return nil, fmt.Errorf("%w: %s %s: %s is %s, pinned %s", ErrChecksum, pin.Name, pin.Version, sumsFile, sum, want)
	}

	if err := keep(kept, data); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", pin.Name, err)
	}

	return data, nil
}

// Listed reads a SHA256SUMS file: each file's checksum by its name.
func Listed(sums []byte) map[string]string {
	out := map[string]string{}

	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		sum, file, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok {
			out[strings.TrimPrefix(strings.TrimSpace(file), "*")] = strings.ToLower(sum)
		}
	}

	return out
}

var errNotFound = errors.New("not found")

func (f Fetcher) download(ctx context.Context, pin Pin, file string) ([]byte, error) {
	pattern := pin.URL
	if pattern == "" {
		pattern = DefaultURL
	}

	u := fmt.Sprintf(pattern, pin.Repository, pin.Version, file)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", pin.Name, err)
	}

	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", pin.Name, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: fetch %s: %s: %w (%s)", ErrPin, pin.Name, u, errNotFound, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: fetch %s: %s: %s", ErrPin, pin.Name, u, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %s: %w", pin.Name, u, err)
	}

	if len(data) > maxFile {
		return nil, fmt.Errorf("%w: fetch %s: %s is over %d bytes", ErrPin, pin.Name, u, maxFile)
	}

	return data, nil
}

const maxFile = 200 << 20

// keep writes data to dest by a rename, so a reader never sees half a file.
func keep(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "download-*")
	if err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone once renamed

	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}

	if err == nil {
		err = os.Chmod(tmp.Name(), 0o750) //nolint:gosec // an executable
	}

	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}

	return err //nolint:wrapcheck // the caller says whose
}

func hexSum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// FileSum is a file's SHA-256 in hex, or empty when it can't be read.
func FileSum(p string) string { return fileSum(p) }

func fileSum(p string) string {
	f, err := os.Open(p) //nolint:gosec // a pinned file in a plugins directory or pipelinegen's cache
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read only

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}

	return hex.EncodeToString(h.Sum(nil))
}

// Accepts are the checksums a file for platform may have under the pin.
func (f Fetcher) Accepts(ctx context.Context, pin Pin, platform string) ([]string, error) {
	files, _, err := f.wanted(ctx, pin, platform)

	out := make([]string, 0, len(files))
	for _, w := range files {
		out = append(out, w.sum)
	}

	return out, err
}
