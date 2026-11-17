package connector

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
	"runtime"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrChecksum is a connector's binary whose checksum isn't the pinned one.
var ErrChecksum = errors.New("connector checksum mismatch")

// ErrPin is a pin no binary can be checked against.
var ErrPin = errors.New("connector pin")

const defaultURL = "https://github.com/%s/releases/download/%s/%s"

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Program is the name of a pinned connector's program, which names its
// release files.
func Program(pin config.ConnectorPin) string { return plugin.Program(pin.Repository) }

// asset is the pinned release's file for this platform.
func asset(pin config.ConnectorPin) string {
	return Program(pin) + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

// Fetch returns the path of the pinned connector's binary: dir's copy
// (the plugins directory a pipeline's bootstrap fills) when it matches the
// pin, cacheDir's when an earlier fetch put it there, and otherwise one
// downloaded into cacheDir and checked against the pin. A binary that
// doesn't match its pin never runs.
func Fetch(ctx context.Context, client *http.Client, dir, cacheDir string, pin config.ConnectorPin) (string, error) {
	want, err := pinnedSum(ctx, client, pin)
	if err != nil {
		return "", err
	}

	if dir != "" {
		if p := filepath.Join(dir, pin.Name); fileSum(p) == want {
			return p, nil
		}
	}

	dest := filepath.Join(cacheDir, "connectors", want, Program(pin))
	if fileSum(dest) == want {
		return dest, nil
	}

	data, err := download(ctx, client, pin, asset(pin))
	if err != nil {
		return "", err
	}

	if sum := hexSum(data); sum != want {
		return "", fmt.Errorf("%w: %s %s: %s is %s, pinned %s", ErrChecksum, pin.Name, pin.Version, asset(pin), sum, want)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", fmt.Errorf("fetch connector %s: %w", pin.Name, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "download-*")
	if err != nil {
		return "", fmt.Errorf("fetch connector %s: %w", pin.Name, err)
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

	if err != nil {
		return "", fmt.Errorf("fetch connector %s: %w", pin.Name, err)
	}

	return dest, nil
}

// pinnedSum is the checksum this platform's binary must have: the pin's,
// or its line in the release's SHA256SUMS once that file matches the pin.
func pinnedSum(ctx context.Context, client *http.Client, pin config.ConnectorPin) (string, error) {
	if pin.Sums == "" {
		sum, ok := pin.SHA256.For(config.Platform())
		if !ok || !sha256Hex.MatchString(strings.ToLower(sum)) {
			return "", fmt.Errorf("%w: connector %s %s has no checksum for %s: pin a release of it under plugins, kind connector",
				ErrPin, pin.Name, pin.Version, config.Platform())
		}

		return strings.ToLower(sum), nil
	}

	sums, err := download(ctx, client, pin, "SHA256SUMS")
	if err != nil {
		return "", err
	}

	if sum := hexSum(sums); sum != strings.ToLower(pin.Sums) {
		return "", fmt.Errorf("%w: %s %s: SHA256SUMS is %s, pinned %s", ErrChecksum, pin.Name, pin.Version, sum, pin.Sums)
	}

	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		sum, file, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok && strings.TrimPrefix(strings.TrimSpace(file), "*") == asset(pin) {
			return strings.ToLower(sum), nil
		}
	}

	return "", fmt.Errorf("%w: %s %s: its SHA256SUMS lists no %s", ErrPin, pin.Name, pin.Version, asset(pin))
}

func download(ctx context.Context, client *http.Client, pin config.ConnectorPin, file string) ([]byte, error) {
	pattern := pin.URL
	if pattern == "" {
		pattern = defaultURL
	}

	u := fmt.Sprintf(pattern, pin.Repository, pin.Version, file)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch connector %s: %w", pin.Name, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch connector %s: %w", pin.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: fetch connector %s: %s: %s", ErrPin, pin.Name, u, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		return nil, fmt.Errorf("fetch connector %s: %s: %w", pin.Name, u, err)
	}

	if len(data) > maxBinary {
		return nil, fmt.Errorf("%w: fetch connector %s: %s is over %d bytes", ErrPin, pin.Name, u, maxBinary)
	}

	return data, nil
}

const maxBinary = 200 << 20

func hexSum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func fileSum(p string) string {
	data, err := os.ReadFile(p) //nolint:gosec // a connector's binary in the plugins directory or pipelinegen's cache
	if err != nil {
		return ""
	}

	return hexSum(data)
}
