package proof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// The releases the proof plans with come through GitHub's API: the token
// goes only in a request's Authorization header, and GITHUB_API names
// another API than https://api.github.com.

var errAPI = errors.New("GitHub's API")

var client = &http.Client{Timeout: 5 * time.Minute}

// get asks the API for path; the token goes in a header, never the URL.
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

func latestOr(ctx context.Context, token, repo, which string) (string, []asset, error) {
	resp, err := get(ctx, token, apiBase()+"/repos/"+repo+"/releases/"+which, "application/vnd.github+json")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var rel struct {
		Tag    string  `json:"tag_name"`
		Assets []asset `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", nil, fmt.Errorf("%s %s: %w", repo, which, err)
	}

	return rel.Tag, rel.Assets, nil
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

// Fetch downloads one asset of a release and keeps it only when its
// checksum is the pinned one. Go's client drops the Authorization header
// when the download redirects to another host.
func Fetch(ctx context.Context, token, repo, tag, name, sum, out string) error {
	_, assets, err := latestOr(ctx, token, repo, "tags/"+tag)
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

// Newest is the newest release's tag and the asset's checksum as that
// release's SHA256SUMS states it.
func Newest(ctx context.Context, token, repo, name string) (string, string, error) {
	tag, assets, err := latestOr(ctx, token, repo, "latest")
	if err != nil {
		return "", "", err
	}

	body, err := download(ctx, token, repo, tag, assets, "SHA256SUMS")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = body.Close() }()

	sums, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("%s %s SHA256SUMS: %w", repo, tag, err)
	}

	for line := range strings.SplitSeq(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return tag, f[0], nil
		}
	}

	return "", "", fmt.Errorf("%w: %s %s: SHA256SUMS names no %s", errAPI, repo, tag, name)
}
