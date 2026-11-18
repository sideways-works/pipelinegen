package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/plugkit/pin"
)

// Why a plugin's binary can't be checked against its pin.
var (
	ErrUnpinned  = errors.New("pinned by commit, with no checksum to check its binary by")
	ErrNoSums    = errors.New("its release is pinned by sums, and no SHA256SUMS that sums pins lies beside it")
	ErrNotListed = errors.New("its release's SHA256SUMS lists no file for this platform")
)

// PinnedSum is the SHA-256 the config's pin gives the plugin's file in dir: its release's sha256,
// or its platform's line of the SHA256SUMS kept beside it once that file matches the pin.
func PinnedSum(p config.Plugin, dir string) (string, error) {
	if p.Release == nil || p.Release.Pin() == "" {
		return "", ErrUnpinned
	}

	if p.Release.Sums == "" {
		return strings.ToLower(p.Release.SHA256), nil
	}

	file := filepath.Join(dir, p.Name+".SHA256SUMS")

	if sum, err := FileSHA256(file); err != nil || sum != strings.ToLower(p.Release.Sums) {
		return "", ErrNoSums
	}

	data, err := os.ReadFile(file) //nolint:gosec // the release's own list, checked by its pin
	if err != nil {
		return "", ErrNoSums
	}

	return sumFor(data, p.Program())
}

func sumFor(sums []byte, name string) (string, error) {
	listed := pin.Listed(sums)

	for _, f := range []string{name + "-" + runtime.GOOS + "-" + runtime.GOARCH, name + ".mjs"} {
		if sum, ok := listed[f]; ok {
			return sum, nil
		}
	}

	return "", fmt.Errorf("%w: %s-%s-%s", ErrNotListed, name, runtime.GOOS, runtime.GOARCH)
}

func FileSHA256(p string) (string, error) { return pin.FileSHA256(p) }

// NotPinned is a plugin whose binary is not the file its pin names, or can't be checked against
// it; its answers are kept by its own digest.
type NotPinned struct {
	Plugin string
	// Want is empty when the config gives no SHA-256 to check by; Why then says why.
	Pin, Want, Found, Why string
}

func (n NotPinned) Line() string {
	const used = "it answers, and its answers are cached apart"

	switch {
	case n.Want == "":
		return fmt.Sprintf("plugin %s can't be checked against its pin (%s): %s; its binary's sha256 is %s: %s", n.Plugin, n.Pin, n.Why, n.Found, used)
	case !strings.HasSuffix(n.Pin, " "+n.Want):
		return fmt.Sprintf("plugin %s is not the build its pin names (%s, which gives its file sha256 %s): its binary's sha256 is %s: %s", n.Plugin, n.Pin, n.Want, n.Found, used)
	default:
		return fmt.Sprintf("plugin %s is not the build its pin names (%s): its binary's sha256 is %s: %s", n.Plugin, n.Pin, n.Found, used)
	}
}

func build(p config.Plugin, dir, exe string) (string, *NotPinned) {
	found, err := binarySum(exe)
	if err != nil {
		return "", nil
	}

	want, err := PinnedSum(p, dir)

	switch {
	case err == nil && found == want:
		return "", nil
	case errors.Is(err, ErrUnpinned):
		return found, nil
	}

	pin := p.Release.Version + ", sha256 " + strings.ToLower(p.Release.SHA256)
	if p.Release.Sums != "" {
		pin = p.Release.Version + ", sums " + strings.ToLower(p.Release.Sums)
	}

	n := &NotPinned{Plugin: p.Name, Pin: pin, Want: want, Found: found}
	if err != nil {
		n.Why = err.Error()
	}

	return found, n
}

// sums caches the binaries' digests by path, size and time: a command loads the plugins several
// times, and a binary is tens of megabytes.
var sums sync.Map

func binarySum(exe string) (string, error) {
	info, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}

	key := fmt.Sprintf("%s\x00%d\x00%d", exe, info.Size(), info.ModTime().UnixNano())
	if v, ok := sums.Load(key); ok {
		if sum, ok := v.(string); ok {
			return sum, nil
		}
	}

	sum, err := FileSHA256(exe)
	if err != nil {
		return "", err
	}

	sums.Store(key, sum)

	return sum, nil
}
