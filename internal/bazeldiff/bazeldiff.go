// Package bazeldiff runs bazel-diff, the only source of what a change
// reaches, and fetches the pinned release of it.
package bazeldiff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/progress"
)

type Tool struct {
	Path string
	// Env is its environment, nil for the process's: the tool gives it one
	// without credentials (secret.ChildEnv).
	Env []string
}

var ErrBazelDiff = errors.New("bazel-diff")

func (t Tool) run(ctx context.Context, dir string, args ...string) error {
	defer progress.Stage(ctx, "bazel-diff %s", args[0])()

	cmd := exec.CommandContext(ctx, t.Path, args...) //nolint:gosec // the pinned bazel-diff, checked by its checksum
	cmd.Dir = dir
	cmd.Env = t.Env

	var out bytes.Buffer

	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w %s: %w: %s", ErrBazelDiff, args[0], err, tail(out.String(), 20))
	}

	return nil
}

func bazelArgs(bz *bazel.Client) []string {
	args := []string{"--workspacePath", bz.Dir, "--bazelPath", bz.Bazelisk}
	if len(bz.StartupFlags) > 0 {
		args = append(args, "--bazelStartupOptions", strings.Join(bz.StartupFlags, " "))
	}

	return args
}

// DepsRepo is the external repository planning writes the slices' bindings into; bazel-diff hashes it file
// by file, so one changed binding reaches only the slices bound to it.
const DepsRepo = "@pipelinegen_deps"

const DepsDir = ".pipelinegen/deps"

// Hashes writes the hashes of the workspace in dir to hashes and its
// dependency edges to edges, running Bazel the way bz does.
func (t Tool) Hashes(ctx context.Context, bz *bazel.Client, dir, hashes, edges string) error {
	cl := *bz
	cl.Dir = dir

	args := append([]string{"generate-hashes", "--depEdgesFile", edges}, bazelArgs(&cl)...)
	if _, err := os.Stat(filepath.Join(dir, DepsDir)); err == nil {
		args = append(args, "--fineGrainedHashExternalRepos", DepsRepo)
	}

	return t.run(ctx, dir, append(args, hashes)...)
}

type explanation struct {
	RootCauses []struct {
		Label string   `json:"label"`
		Path  []string `json:"path"`
	} `json:"rootCauses"` //nolint:tagliatelle // bazel-diff's field name
}

// Explain names the targets whose own change made target differ, nearest
// first, with the path from each to it.
func (t Tool) Explain(ctx context.Context, base, head, headEdges, target string) ([]affected.RootCause, error) {
	out := head + ".explain-" + strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(target) + ".json"
	defer removeQuietly(out)

	if err := t.run(ctx, filepath.Dir(head), "explain", "--startingHashes", base, "--finalHashes", head,
		"--depEdgesFile", headEdges, "--target", target, "--format", "json", "--maxRootCauses", "3", "--output", out); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(out) //nolint:gosec // the file made just above
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}

	var e explanation
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("%w: explain: %w", ErrBazelDiff, err)
	}

	causes := make([]affected.RootCause, 0, len(e.RootCauses))
	for _, c := range e.RootCauses {
		causes = append(causes, affected.RootCause{Label: c.Label, Path: c.Path})
	}

	return causes, nil
}

// Impacted lists the labels of the targets that differ between two hash
// files; bz is the workspace bazel-diff may query.
func (t Tool) Impacted(ctx context.Context, bz *bazel.Client, base, head string) ([]string, error) {
	f, err := os.CreateTemp(filepath.Dir(head), "impacted-*.txt")
	if err != nil {
		return nil, fmt.Errorf("impacted: %w", err)
	}

	name := f.Name()
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("impacted: %w", err)
	}

	defer removeQuietly(name)

	args := append([]string{"get-impacted-targets", "--startingHashes", base, "--finalHashes", head, "--output", name}, bazelArgs(bz)...)
	if err := t.run(ctx, bz.Dir, args...); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(name) //nolint:gosec // the file made just above
	if err != nil {
		return nil, fmt.Errorf("impacted: %w", err)
	}

	var labels []string

	for line := range strings.SplitSeq(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			labels = append(labels, line)
		}
	}

	return labels, nil
}

// Release names one bazel-diff release and its binaries' checksums, by
// platform.
type Release struct {
	Version string
	SHA256  config.Checksums
	// URL overrides where it is fetched from; %s is replaced by the version
	// and the platform's asset name, in that order.
	URL string
}

// Pinned is the bazel-diff release pipelinegen runs unless the config names
// another; it is checked under Bazel 9.2.0.
var Pinned = Release{Version: config.Pinned.Version, SHA256: config.Pinned.SHA256}

const defaultURL = "https://github.com/Tinder/bazel-diff/releases/download/%s/%s"

var ErrChecksum = errors.New("checksum mismatch")

// Fetch returns the release's binary, from cacheDir when an earlier fetch
// put it there, downloaded and checked against its checksum otherwise.
func Fetch(ctx context.Context, client *http.Client, cacheDir string, r Release) (Tool, error) {
	want, err := r.checksum()
	if err != nil {
		return Tool{}, err
	}

	dest := filepath.Join(cacheDir, "bazel-diff", want, "bazel-diff")

	if sum, err := fileSum(dest); err == nil && sum == want {
		return Tool{Path: dest}, nil
	}

	pattern := r.URL
	if pattern == "" {
		pattern = defaultURL
	}

	url := fmt.Sprintf(pattern, r.Version, asset())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Tool{}, fmt.Errorf("%w: fetch %s: %s", ErrBazelDiff, url, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "download-*")
	if err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}
	defer removeQuietly(tmp.Name())

	h := sha256.New()

	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}

	if sum := hex.EncodeToString(h.Sum(nil)); sum != want {
		return Tool{}, fmt.Errorf("%w: %s is %s, pinned %s", ErrChecksum, url, sum, want)
	}

	if err := os.Chmod(tmp.Name(), 0o750); err != nil { //nolint:gosec // an executable
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}

	if err := os.Rename(tmp.Name(), dest); err != nil {
		return Tool{}, fmt.Errorf("fetch bazel-diff: %w", err)
	}

	return Tool{Path: dest}, nil
}

func fileSum(p string) (string, error) {
	data, err := os.ReadFile(p) //nolint:gosec // a file in pipelinegen's own cache
	if err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func removeQuietly(p string) {
	if err := os.Remove(p); err != nil {
		return
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}

// asset is the release file for this platform: bazel-diff names macOS
// "macos".
func asset() string { return assetFor(runtime.GOOS, runtime.GOARCH) }

func assetFor(goos, goarch string) string {
	if goos == "darwin" {
		goos = "macos"
	}

	return "bazel-diff-rust-" + goos + "-" + goarch
}

func (r Release) checksum() (string, error) {
	want, ok := r.SHA256.For(config.Platform())

	switch {
	case r.Version == "":
		return "", fmt.Errorf("%w: release needs a version and a sha256", ErrBazelDiff)
	case !ok:
		return "", fmt.Errorf("%w: bazel_diff %s has no checksum for %s: bazel-diff publishes no build for it, or the config names none; "+
			"give its checksum in bazel_diff.sha256 by platform, or a bazel-diff built here with --bazel-diff", ErrBazelDiff, r.Version, config.Platform())
	case len(want) != 64:
		return "", fmt.Errorf("%w: release needs a version and a sha256", ErrBazelDiff)
	}

	return want, nil
}
