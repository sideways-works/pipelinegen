package proof

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/forge"
	"github.com/sideways-works/plugkit/pin"
)

// The releases come through GitHub's API: the token goes only in a request's Authorization header, and
// GITHUB_API names another API than https://api.github.com.

var errAPI = errors.New("GitHub's API")

var client = &http.Client{Timeout: 5 * time.Minute}

func get(ctx context.Context, token, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}

	req.Header.Set("Accept", accept)
	req.Header.Set("X-Github-Api-Version", "2022-11-28")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", req.URL.Path, err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: GET %s: %s", errAPI, req.URL.Path, resp.Status)
	}

	return resp, nil
}

func apiBase() string {
	if api := strings.TrimRight(os.Getenv("GITHUB_API"), "/"); api != "" {
		return api
	}

	return "https://api.github.com"
}

type asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func tagged(ctx context.Context, token, repo, tag string) ([]asset, error) {
	resp, err := get(ctx, token, apiBase()+"/repos/"+repo+"/releases/tags/"+tag, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var rel struct {
		Assets []asset `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("%s %s: %w", repo, tag, err)
	}

	return rel.Assets, nil
}

func download(ctx context.Context, token, repo, tag string, assets []asset, name string) (io.ReadCloser, error) {
	i := slices.IndexFunc(assets, func(a asset) bool { return a.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s %s has no asset %s", errAPI, repo, tag, name)
	}

	resp, err := get(ctx, token, fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBase(), repo, assets[i].ID), "application/octet-stream")
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

// Fetch downloads a release's asset and keeps it only when its checksum is the pinned one; Go's client
// drops the Authorization header when the download redirects to another host.
func Fetch(ctx context.Context, token, repo, tag, name, sum, out string) error {
	assets, err := tagged(ctx, token, repo, tag)
	if err != nil {
		return err
	}

	body, err := download(ctx, token, repo, tag, assets, name)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	tmp := out + ".part"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) //nolint:gosec // an executable the caller names, kept only when its checksum is the pin
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), body)

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return errors.Join(fmt.Errorf("%s %s %s: %w", repo, tag, name, err), os.Remove(tmp))
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return errors.Join(fmt.Errorf("%w: %s %s %s: checksum %s differs from the pin %s", errChecksum, repo, tag, name, got, sum), os.Remove(tmp))
	}

	if err := os.Rename(tmp, out); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	return nil
}

// Newest is the newest release of the program at dir in repo (none for a repository of one
// program) that is no prerelease: its version and the asset's checksum as its SHA256SUMS states it.
// The listing goes through the forge client, which sends GITHUB_TOKEN to GitHub's API alone.
func Newest(ctx context.Context, repo, dir, name string) (string, string, error) {
	c := forge.Client()

	rs, err := forge.Releases(ctx, c, repo, dir)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", errAPI, err)
	}

	rel, ok := forge.Newest(rs, false)
	if !ok {
		return "", "", fmt.Errorf("%w: %s has no release of %s", errAPI, repo, cmp.Or(dir, name))
	}

	sums, err := forge.Download(ctx, c, rel, "SHA256SUMS")
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", errAPI, err)
	}

	sum, ok := pin.Listed(sums)[name]
	if !ok {
		return "", "", fmt.Errorf("%w: %s %s: SHA256SUMS names no %s", errAPI, repo, rel.Tag, name)
	}

	return path.Base(rel.Tag), sum, nil
}
