// Package forgetest is a GitHub and a GitLab of the tests' own: their APIs' release listings and
// their release files, for every repository the test adds releases to.
package forgetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/forge"
)

type release struct {
	tag        string
	prerelease bool
	draft      bool
	files      map[string][]byte
}

// GitHub serves github.com, api.github.com and gitlab.com, each project's releases on both;
// forge.Client reaches it once Start set forge.EnvTestURL.
type GitHub struct {
	URL string
	// PerPage is how many releases a page of the listing holds, so a test sees several pages.
	PerPage int

	mu       sync.Mutex
	releases map[string][]release
	asked    []string
}

func Start(t *testing.T) *GitHub {
	t.Helper()

	g := &GitHub{releases: map[string][]release{}, PerPage: 2}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)

	g.URL = srv.URL
	t.Setenv(forge.EnvTestURL, srv.URL)

	return g
}

// Release adds a release of project (owner/name) under tag with files, listed in a SHA256SUMS of
// its own unless files holds one. It returns that list's checksum.
func (g *GitHub) Release(project, tag string, prerelease bool, files map[string][]byte) string {
	files = cloneFiles(files)

	if _, ok := files[forge.SumsFile]; !ok {
		var b strings.Builder

		for _, name := range sortedNames(files) {
			fmt.Fprintf(&b, "%s  %s\n", Sum(files[name]), name)
		}

		files[forge.SumsFile] = []byte(b.String())
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.releases[project] = append(g.releases[project], release{tag: tag, prerelease: prerelease, files: files})

	return Sum(files[forge.SumsFile])
}

// Draft adds a draft release, which the API lists and no pin may take.
func (g *GitHub) Draft(project, tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.releases[project] = append(g.releases[project], release{tag: tag, draft: true, files: map[string][]byte{}})
}

// Asked are the paths of the release files downloaded so far.
func (g *GitHub) Asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.asked)
}

func Sum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func (g *GitHub) serve(w http.ResponseWriter, req *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case req.Host == "api.github.com" && strings.HasPrefix(req.URL.Path, "/repos/") && strings.HasSuffix(req.URL.Path, "/releases"):
		g.list(w, req, strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/repos/"), "/releases"))
	case req.Host == "github.com" && strings.Contains(req.URL.Path, "/releases/download/"):
		project, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/releases/download/")
		i := strings.LastIndex(rest, "/")
		g.download(w, req, project, rest[:max(i, 0)], rest[i+1:])
	case req.Host == gitLab && strings.HasPrefix(req.URL.Path, "/api/v4/projects/") && strings.HasSuffix(req.URL.Path, "/releases"):
		g.list(w, req, strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/api/v4/projects/"), "/releases"))
	case req.Host == gitLab && strings.Contains(req.URL.Path, "/-/releases/"):
		project, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/-/releases/")
		i := strings.LastIndex(rest, "/downloads/")
		g.download(w, req, project, rest[:max(i, 0)], rest[i+len("/downloads/"):])
	default:
		http.NotFound(w, req)
	}
}

func (g *GitHub) download(w http.ResponseWriter, req *http.Request, project, tag, file string) {
	for _, r := range g.releases[project] {
		if data, ok := r.files[file]; ok && r.tag == tag && !r.draft {
			g.asked = append(g.asked, req.URL.Path)
			_, _ = w.Write(data) //nolint:errcheck // a test server

			return
		}
	}

	http.NotFound(w, req)
}

const gitLab = "gitlab.com"

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type listedRelease struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

// list answers newest first, as GitHub does, a page at a time.
func (g *GitHub) list(w http.ResponseWriter, req *http.Request, project string) {
	rs, ok := g.releases[project]
	if !ok {
		http.NotFound(w, req)

		return
	}

	page, _ := strconv.Atoi(req.URL.Query().Get("page")) //nolint:errcheck // no page is the first
	page = max(page, 1)

	var all []any

	for _, r := range slices.Backward(rs) {
		if req.Host == gitLab {
			all = append(all, gitLabRelease(project, r))

			continue
		}

		lr := listedRelease{Tag: r.tag, Draft: r.draft, Prerelease: r.prerelease, Assets: []asset{}}
		for _, name := range sortedNames(r.files) {
			lr.Assets = append(lr.Assets, asset{Name: name, URL: "https://github.com/" + project + "/releases/download/" + r.tag + "/" + name})
		}

		all = append(all, lr)
	}

	from, to := min((page-1)*g.PerPage, len(all)), min(page*g.PerPage, len(all))
	if to < len(all) {
		next := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", project, page+1)
		if req.Host == gitLab {
			next = fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/releases?per_page=100&page=%d", url.PathEscape(project), page+1)
		}

		w.Header().Set("Link", "<"+next+`>; rel="next"`)
	}

	_ = json.NewEncoder(w).Encode(all[from:to]) //nolint:errcheck,errchkjson // a test server
}

// gitLabRelease is a release as GitLab's API lists it: a release yet to come is its prerelease,
// and each file a link to its download.
func gitLabRelease(project string, r release) map[string]any {
	links := make([]map[string]string, 0, len(r.files))
	for _, name := range sortedNames(r.files) {
		links = append(links, map[string]string{
			"name": name, "direct_asset_url": "https://gitlab.com/" + project + "/-/releases/" + url.PathEscape(r.tag) + "/downloads/" + name,
		})
	}

	return map[string]any{"tag_name": r.tag, "upcoming_release": r.prerelease, "assets": map[string]any{"links": links}}
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}

	slices.Sort(names)

	return names
}

func cloneFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	maps.Copy(out, files)

	return out
}

// Plugin is a release's files of program: one per platform, each telling the version apart, and
// the plugin.json its describe answer is released as.
func Plugin(program, name, kind, version string, platforms ...string) map[string][]byte {
	files := map[string][]byte{}
	for _, p := range platforms {
		files[program+"-"+p] = []byte("#!/bin/sh\n# " + program + " " + version + " for " + p + "\n")
	}

	files["plugin.json"] = []byte(`{"protocol": 1, "name": "` + name + `", "kind": "` + kind + `", "version": "` + version + `"}` + "\n")

	return files
}
