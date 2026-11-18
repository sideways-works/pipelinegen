package forge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/forge"
	"github.com/sideways-works/pipelinegen/internal/forge/forgetest"
	"github.com/sideways-works/plugkit/fetch"
	"github.com/sideways-works/plugkit/pin"
)

func TestATagGivesAVersionToThePluginAtItsPath(t *testing.T) {
	for _, c := range []struct {
		tag, dir, want string
	}{
		{"v1.2.3", "", "1.2.3"},
		{"v1.2.3-rc.1", "", "1.2.3-rc.1"},
		{"driver/go/v1.2.3", "", ""},
		{"driver/go/v1.2.3", "driver/go", "1.2.3"},
		{"driver/gox/v1.2.3", "driver/go", ""},
		{"driver/go/x/v1.2.3", "driver/go", ""},
		{"v1.2.3", "driver/go", ""},
		{"1.2.3", "", ""},
		{"vnext", "", ""},
	} {
		v, ok := forge.VersionOf(c.tag, c.dir)
		if got := map[bool]string{true: v.String()}[ok]; got != c.want {
			t.Errorf("%s at %q: %q, want %q", c.tag, c.dir, got, c.want)
		}
	}
}

func newestOf(t *testing.T, project, dir string, pre bool) string {
	t.Helper()

	rs, err := forge.Releases(t.Context(), forge.Client(), "https://github.com/"+project, dir)
	if err != nil {
		t.Fatal(err)
	}

	r, ok := forge.Newest(rs, pre)
	if !ok {
		return ""
	}

	return r.Tag
}

func TestTheNewestReleaseSkipsPrereleasesUnlessAsked(t *testing.T) {
	g := forgetest.Start(t)

	for _, r := range []struct {
		tag string
		pre bool
	}{
		{"driver/go/v0.1.0", false},
		{"sbom/go/v0.9.0", false},
		{"driver/go/v0.2.1", false},
		{"v3.0.0", false},
		{"driver/go/v0.3.0-rc.1", false},
		{"driver/go/v0.2.2", true},
		{"driver/go/v0.10.0-beta", false},
	} {
		g.Release("o/plugins", r.tag, r.pre, map[string][]byte{"x": nil})
	}

	g.Draft("o/plugins", "driver/go/v9.0.0")

	if got := newestOf(t, "o/plugins", "driver/go", false); got != "driver/go/v0.2.1" {
		t.Errorf("newest release: %s", got)
	}

	if got := newestOf(t, "o/plugins", "driver/go", true); got != "driver/go/v0.10.0-beta" {
		t.Errorf("newest with prereleases: %s", got)
	}

	if got := newestOf(t, "o/plugins", "", false); got != "v3.0.0" {
		t.Errorf("the repository's own: %s", got)
	}

	if got := newestOf(t, "o/plugins", "sbom/rust", true); got != "" {
		t.Errorf("a path with no release: %s", got)
	}
}

func TestEveryFormOfARepositoryListsTheSameReleases(t *testing.T) {
	g := forgetest.Start(t)
	g.Release("o/plugins", "driver/go/v0.2.1", false, map[string][]byte{"x": nil})

	for _, repo := range []string{"o/plugins", "https://github.com/o/plugins", "https://github.com/o/plugins.git", "git@github.com:o/plugins.git"} {
		rs, err := forge.Releases(t.Context(), forge.Client(), repo, "driver/go")
		if err != nil || len(rs) != 1 || rs[0].Tag != "driver/go/v0.2.1" {
			t.Errorf("%s: %v %+v", repo, err, rs)
		}
	}

	if _, err := forge.Releases(t.Context(), forge.Client(), "https://git.example.com/o/plugins", "driver/go"); err == nil || !strings.Contains(err.Error(), "a plain git server") {
		t.Errorf("a plain git server: %v", err)
	}
}

func checker(t *testing.T) forge.Checker {
	t.Helper()

	return forge.Checker{Client: forge.Client(), Fetcher: fetch.Fetcher{Client: forge.Client(), Cache: t.TempDir(), Under: "releases"}, Work: t.TempDir()}
}

func release(t *testing.T, project, dir string) forge.Release {
	t.Helper()

	rs, err := forge.Releases(t.Context(), forge.Client(), project, dir)
	if err != nil || len(rs) == 0 {
		t.Fatalf("%v %+v", err, rs)
	}

	r, _ := forge.Newest(rs, true)

	return r
}

// A plugin of a monorepo is fetched from the release its path's tag names: every platform's file,
// each checked by its SHA256SUMS line.
func TestAPathPluginsReleaseIsCheckedFileByFile(t *testing.T) {
	g := forgetest.Start(t)
	sums := g.Release("o/plugins", "driver/go/v0.2.1", false, forgetest.Plugin("go-driver", "go-driver", "driver", "v0.2.1", "linux-amd64", "darwin-arm64"))
	g.Release("o/plugins", "sbom/go/v0.2.1", false, forgetest.Plugin("go-sbom", "go-sbom", "sbom", "v0.2.1", "linux-amd64"))

	c, err := checker(t).Check(t.Context(), "https://github.com/o/plugins", "driver/go", "go-driver", release(t, "https://github.com/o/plugins", "driver/go"))
	if err != nil {
		t.Fatal(err)
	}

	if c.Version != "v0.2.1" || c.Sums != sums || c.Name != "go-driver" || c.Kind != "driver" || len(c.Files) != 2 {
		t.Fatalf("checked %+v", c)
	}

	asked := strings.Join(g.Asked(), "\n")
	for _, want := range []string{
		"/o/plugins/releases/download/driver/go/v0.2.1/SHA256SUMS",
		"/o/plugins/releases/download/driver/go/v0.2.1/go-driver-linux-amd64",
		"/o/plugins/releases/download/driver/go/v0.2.1/go-driver-darwin-arm64",
		"/o/plugins/releases/download/driver/go/v0.2.1/plugin.json",
	} {
		if !strings.Contains(asked, want) {
			t.Errorf("never fetched %s:\n%s", want, asked)
		}
	}

	if strings.Contains(asked, "sbom/go") {
		t.Errorf("fetched another plugin's release:\n%s", asked)
	}
}

func TestAReleaseWhoseFileIsNotItsListsIsRefused(t *testing.T) {
	g := forgetest.Start(t)
	files := forgetest.Plugin("go-driver", "go-driver", "driver", "v1.0.0", "linux-amd64", "darwin-arm64")
	files[forge.SumsFile] = []byte(forgetest.Sum(files["go-driver-linux-amd64"]) + "  go-driver-linux-amd64\n" + forgetest.Sum([]byte("another build")) + "  go-driver-darwin-arm64\n")
	g.Release("o/r", "v1.0.0", false, files)

	_, err := checker(t).Check(t.Context(), "o/r", "", "go-driver", release(t, "o/r", ""))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), "go-driver-darwin-arm64") {
		t.Fatal(err)
	}
}

func TestAReleaseWithoutItsListIsRefused(t *testing.T) {
	g := forgetest.Start(t)
	g.Release("o/r", "v1.0.0", false, map[string][]byte{forge.SumsFile: nil})

	rs, err := forge.Releases(t.Context(), forge.Client(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}

	rs[0].Assets = map[string]string{}

	if _, err := checker(t).Check(t.Context(), "o/r", "", "r", rs[0]); err == nil || !strings.Contains(err.Error(), "has no file SHA256SUMS") {
		t.Fatal(err)
	}
}

// Without plugin.json the plugin is asked, in this platform's build.
func TestAReleaseWithoutPluginJSONIsAsked(t *testing.T) {
	g := forgetest.Start(t)
	files := map[string][]byte{
		"x-sbom-" + pin.Platform(): []byte("#!/bin/sh\ncat >/dev/null\necho '{\"protocol\": 1, \"name\": \"x-sbom\", \"kind\": \"sbom\", \"version\": \"v1.0.0\"}'\n"),
	}
	g.Release("o/x", "v1.0.0", false, files)

	c, err := checker(t).Check(t.Context(), "o/x", "", "x-sbom", release(t, "o/x", ""))
	if err != nil || c.Name != "x-sbom" || c.Kind != "sbom" {
		t.Fatalf("%v %+v", err, c)
	}
}

// A plugin of a monorepo on GitLab: its releases listed a page at a time by the project's escaped
// path, one yet to come taken as a prerelease, and its files fetched by the escaped tag.
func TestAPathPluginOnGitLabIsListedAndChecked(t *testing.T) {
	g := forgetest.Start(t)
	repo := "https://gitlab.com/group/sub/plugins"

	g.Release("group/sub/plugins", "driver/go/v0.1.0", false, forgetest.Plugin("go-driver", "go-driver", "driver", "v0.1.0", "linux-amd64"))
	sums := g.Release("group/sub/plugins", "driver/go/v0.2.0", false, forgetest.Plugin("go-driver", "go-driver", "driver", "v0.2.0", "linux-amd64", "darwin-arm64"))
	g.Release("group/sub/plugins", "sbom/go/v0.9.0", false, forgetest.Plugin("go-sbom", "go-sbom", "sbom", "v0.9.0", "linux-amd64"))
	g.Release("group/sub/plugins", "driver/go/v0.3.0", true, forgetest.Plugin("go-driver", "go-driver", "driver", "v0.3.0", "linux-amd64"))

	for _, spelling := range []string{repo, "git@gitlab.com:group/sub/plugins.git"} {
		rs, err := forge.Releases(t.Context(), forge.Client(), spelling, "driver/go")
		if err != nil || len(rs) != 3 {
			t.Fatalf("%s: %v %+v", spelling, err, rs)
		}

		if r, _ := forge.Newest(rs, false); r.Tag != "driver/go/v0.2.0" {
			t.Fatalf("%s: newest %s", spelling, r.Tag)
		}

		if r, _ := forge.Newest(rs, true); r.Tag != "driver/go/v0.3.0" {
			t.Fatalf("%s: newest with prereleases %s", spelling, r.Tag)
		}
	}

	rs, err := forge.Releases(t.Context(), forge.Client(), repo, "driver/go")
	if err != nil {
		t.Fatal(err)
	}

	r, _ := forge.Newest(rs, false)

	c, err := checker(t).Check(t.Context(), repo, "driver/go", "go-driver", r)
	if err != nil || c.Sums != sums || c.Kind != "driver" || len(c.Files) != 2 {
		t.Fatalf("%v %+v", err, c)
	}

	if asked := strings.Join(g.Asked(), "\n"); !strings.Contains(asked, "/group/sub/plugins/-/releases/driver/go/v0.2.0/downloads/go-driver-darwin-arm64") {
		t.Fatalf("fetched:\n%s", asked)
	}
}

// GITHUB_TOKEN goes to GitHub's API alone, as its Authorization header: never to the host a
// listing redirects to, nor to a release file's, nor into an error.
func TestTheTokenGoesToGitHubsAPIAlone(t *testing.T) {
	const token = "test-token-not-real"

	var (
		mu   sync.Mutex
		seen = map[string][]string{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()

		seen[req.Host] = append(seen[req.Host], req.Header.Get("Authorization"))
		mu.Unlock()

		switch req.Host {
		case "api.github.com":
			http.Redirect(w, req, "https://elsewhere.example.com/o/r/releases", http.StatusFound)
		case "elsewhere.example.com":
			_, _ = w.Write([]byte(`[{"tag_name": "v1.0.0", "assets": [{"name": "x", "browser_download_url": "https://github.com/o/r/releases/download/v1.0.0/x"}]}]`)) //nolint:errcheck // a test server
		case "github.com":
			http.Redirect(w, req, "https://api.github.com.example.com/x", http.StatusFound)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(forge.EnvTestURL, srv.URL)
	t.Setenv("GITHUB_TOKEN", token)

	rs, err := forge.Releases(t.Context(), forge.Client(), "o/r", "")
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %+v", err, rs)
	}

	_, err = forge.Download(t.Context(), forge.Client(), rs[0], "x")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("download: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	for host, auths := range seen {
		for _, a := range auths {
			if want := map[bool]string{true: "Bearer " + token}[host == "api.github.com"]; a != want {
				t.Errorf("%s was sent %q", host, a)
			}
		}
	}

	if len(seen["api.github.com"]) != 1 || len(seen["elsewhere.example.com"]) != 1 || len(seen["github.com"]) != 1 || len(seen["api.github.com.example.com"]) != 1 {
		t.Fatalf("asked %v", seen)
	}
}
