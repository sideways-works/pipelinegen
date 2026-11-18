// Package forge lists a plugin's releases on the git server that holds them, picks the newest,
// and checks a release by downloading every file a pin of it would name.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/plugkit/pin"
	"github.com/sideways-works/plugkit/semver"
)

// EnvTestURL sends every request for a forge to a server of the tests' own instead, keeping the
// forge's host name in the request's Host.
const EnvTestURL = "PIPELINEGEN_TEST_FORGE_URL"

var (
	ErrNoRelease = errors.New("no release")
	ErrListing   = errors.New("can't list releases")
)

// Client is the HTTP client for the forges: a bounded one, or in tests one that reaches the
// tests' server.
func Client() *http.Client {
	next := http.DefaultTransport

	if u, err := url.Parse(os.Getenv(EnvTestURL)); err == nil && u.Host != "" {
		next = rerouted{to: u}
	}

	return &http.Client{Timeout: 5 * time.Minute, Transport: gitHubToken{next: next}}
}

// gitHubAPI is the one host GITHUB_TOKEN is sent to.
const gitHubAPI = "api.github.com"

// gitHubToken adds GITHUB_TOKEN to each request for GitHub's API over https, a redirect's
// included, and to no other: the client's own requests never carry it, so no redirect copies it.
type gitHubToken struct{ next http.RoundTripper }

func (g gitHubToken) RoundTrip(req *http.Request) (*http.Response, error) {
	token := os.Getenv("GITHUB_TOKEN")

	switch {
	case req.URL.Scheme == "https" && req.URL.Host == gitHubAPI && token != "":
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	case req.Header.Get("Authorization") != "":
		req = req.Clone(req.Context())
		req.Header.Del("Authorization")
	}

	return g.next.RoundTrip(req) //nolint:wrapcheck // the transport's own error
}

type rerouted struct{ to *url.URL }

func (r rerouted) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Host = req.URL.Host
	out.URL.Scheme, out.URL.Host = r.to.Scheme, r.to.Host

	return http.DefaultTransport.RoundTrip(out) //nolint:wrapcheck // the transport's own error
}

// Release is one release of a plugin: its tag, the version the tag gives, and its files by name.
type Release struct {
	Tag        string
	Version    semver.Version
	Prerelease bool
	Assets     map[string]string
}

// VersionOf is the version a tag gives a plugin at dir: <dir>/vX.Y.Z, or vX.Y.Z with no dir. A
// repository plugin never takes a tag with a slash, which is a monorepo plugin's.
func VersionOf(tag, dir string) (semver.Version, bool) {
	rest := tag

	if dir != "" {
		var ok bool
		if rest, ok = strings.CutPrefix(tag, dir+"/"); !ok {
			return semver.Version{}, false
		}
	}

	if !strings.HasPrefix(rest, "v") || strings.Contains(rest, "/") {
		return semver.Version{}, false
	}

	v, err := semver.Parse(rest)

	return v, err == nil
}

// Newest is the release of the highest version, a prerelease only when pre allows one.
func Newest(rs []Release, pre bool) (Release, bool) {
	var (
		best  Release
		found bool
	)

	for _, r := range rs {
		if r.Prerelease && !pre {
			continue
		}

		if !found || r.Version.Compare(best.Version) > 0 {
			best, found = r, true
		}
	}

	return best, found
}

// Releases lists the releases of the plugin at dir in repository: those whose tag gives it a
// version, drafts left out.
func Releases(ctx context.Context, c *http.Client, repository, dir string) ([]Release, error) {
	repo, err := pin.ParseRepository(repository, nil)
	if err != nil {
		return nil, fmt.Errorf("%w of %s: %w", ErrListing, repository, err)
	}

	var (
		next string
		read func([]byte) ([]listed, error)
	)

	switch repo.Forge {
	case pin.GitHub:
		next = apiOrigin(repo) + "/repos/" + repo.Project + "/releases?per_page=100"
		read = gitHubReleases
	case pin.GitLab:
		next = repo.Origin + "/api/v4/projects/" + url.PathEscape(repo.Project) + "/releases?per_page=100"
		read = gitLabReleases
	default:
		return nil, fmt.Errorf("%w of %s: a plain git server, with no release files to fetch; pipelinegen fetches from GitHub and GitLab", ErrListing, repository)
	}

	var out []Release

	for pages := 0; next != "" && pages < maxPages; pages++ {
		body, link, err := get(ctx, c, next)
		if err != nil {
			return nil, fmt.Errorf("%w of %s: %w", ErrListing, repository, err)
		}

		page, err := read(body)
		if err != nil {
			return nil, fmt.Errorf("%w of %s: %w", ErrListing, repository, err)
		}

		for _, l := range page {
			if v, ok := VersionOf(l.tag, dir); ok && !l.draft {
				out = append(out, Release{Tag: l.tag, Version: v, Prerelease: l.pre || v.Prerelease(), Assets: l.assets})
			}
		}

		next = nextPage(link)
	}

	return out, nil
}

// maxPages bounds a listing at 5,000 releases.
const maxPages = 50

func apiOrigin(repo pin.Repo) string {
	if repo.Origin == "https://github.com" {
		return "https://" + gitHubAPI
	}

	return repo.Origin + "/api/v3"
}

type listed struct {
	tag        string
	draft, pre bool
	assets     map[string]string
}

func gitHubReleases(body []byte) ([]listed, error) {
	var rs []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("the releases: %w", err)
	}

	out := make([]listed, 0, len(rs))

	for _, r := range rs {
		l := listed{tag: r.Tag, draft: r.Draft, pre: r.Prerelease, assets: map[string]string{}}
		for _, a := range r.Assets {
			l.assets[a.Name] = a.URL
		}

		out = append(out, l)
	}

	return out, nil
}

func gitLabReleases(body []byte) ([]listed, error) {
	var rs []struct {
		Tag      string `json:"tag_name"`
		Upcoming bool   `json:"upcoming_release"`
		Assets   struct {
			Links []struct {
				Name string `json:"name"`
				URL  string `json:"direct_asset_url"`
			} `json:"links"`
		} `json:"assets"`
	}

	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("the releases: %w", err)
	}

	out := make([]listed, 0, len(rs))

	for _, r := range rs {
		l := listed{tag: r.Tag, pre: r.Upcoming, assets: map[string]string{}}
		for _, a := range r.Assets.Links {
			l.assets[a.Name] = a.URL
		}

		out = append(out, l)
	}

	return out, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextPage(link string) string {
	if m := nextLink.FindStringSubmatch(link); m != nil {
		return m[1]
	}

	return ""
}

// maxBody bounds what a listing or a download may read.
const maxBody = 200 << 20

func get(ctx context.Context, c *http.Client, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // the caller says what was asked
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // the caller says what was asked
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: %s", u, resp.Status) //nolint:err113 // the server's answer
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", u, err)
	}

	if len(body) > maxBody {
		return nil, "", fmt.Errorf("%s: over %d bytes", u, maxBody) //nolint:err113 // a bound of ours
	}

	return body, resp.Header.Get("Link"), nil
}

// Download reads one file of a release.
func Download(ctx context.Context, c *http.Client, r Release, file string) ([]byte, error) {
	u, ok := r.Assets[file]
	if !ok {
		names := make([]string, 0, len(r.Assets))
		for n := range r.Assets {
			names = append(names, n)
		}

		slices.Sort(names)

		return nil, fmt.Errorf("%w: release %s has no file %s (it has %s)", ErrNoRelease, r.Tag, file, strings.Join(names, ", "))
	}

	data, _, err := get(ctx, c, u)
	if err != nil {
		return nil, fmt.Errorf("release %s: %w", r.Tag, err)
	}

	return data, nil
}
