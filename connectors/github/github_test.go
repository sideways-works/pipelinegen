package github

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func pullsHost(t *testing.T, pulls string) *fakeGitHub {
	t.Helper()

	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/pulls":
			write(w, pulls)
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			write(w, `[{"event": "ready_for_review", "created_at": "2026-02-01T00:00:00Z"}, {"event": "labeled", "created_at": "2026-03-01T00:00:00Z"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)

	return f
}

func listChanges(t *testing.T, f *fakeGitHub, args plugin.ChangesArgs) []plugin.Change {
	t.Helper()

	var a plugin.ChangesAnswer
	if err := json.Unmarshal(drive(t, f, plugin.CallChanges, args), &a); err != nil {
		t.Fatal(err)
	}

	return a.Changes
}

func TestWhereAPullRequestComesFrom(t *testing.T) {
	pull := func(n, headID int, headName string) string {
		head := `{"id": ` + itoa(headID) + `, "full_name": "` + headName + `"}`
		if headID == 0 {
			head = "null"
		}

		return `{"number": ` + itoa(n) + `, "draft": ` + map[bool]string{true: "true", false: "false"}[n == 4] + `, "labels": [{"name": "ready"}],
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
			"head": {"sha": "` + strings.Repeat(itoa(n), 40) + `", "ref": "b` + itoa(n) + `", "repo": ` + head + `},
			"base": {"sha": "", "ref": "main", "repo": {"id": 1, "full_name": "o/r"}}}`
	}

	f := pullsHost(t, "["+strings.Join([]string{pull(1, 1, "o/r"), pull(2, 9, "someone/r"), pull(3, 5, "o/r"), pull(4, 0, "")}, ",")+"]")

	got := listChanges(t, f, plugin.ChangesArgs{Base: "main"})

	want := map[string]string{"pr-1": plugin.FromSame, "pr-2": plugin.FromFork, "pr-3": plugin.FromFork, "pr-4": plugin.FromUnknown}
	if len(got) != len(want) {
		t.Fatalf("changes: %+v", got)
	}

	for _, c := range got {
		if c.From != want[c.ID] {
			t.Errorf("%s from %q, want %q", c.ID, c.From, want[c.ID])
		}

		if c.Base != "main" || len(c.Head) != 40 || c.Labels[0] != "ready" || c.ReadySince != nil {
			t.Errorf("%s: %+v", c.ID, c)
		}
	}

	if !got[3].Draft || got[0].Draft {
		t.Errorf("drafts: %+v", got)
	}
}

func TestReadySinceComesFromTheTimeline(t *testing.T) {
	f := pullsHost(t, `[{"number": 7, "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "`+headSHA+`", "ref": "b", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}}]`)

	got := listChanges(t, f, plugin.ChangesArgs{Base: "main", Filters: plugin.ChangesFilters{ReadySince: true}})
	if len(got) != 1 || got[0].ReadySince == nil || got[0].ReadySince.Format("2006-01-02") != "2026-02-01" {
		t.Fatalf("%+v", got)
	}
}

func TestIdentify(t *testing.T) {
	gh := Config{API: DefaultAPI}
	for remote, want := range map[string]string{
		"https://github.com/o/r.git": "o/r", "git@github.com:o/r.git": "o/r", "ssh://git@github.com/o/r": "o/r",
		"https://gitlab.example.test/o/r.git": "", "https://github.com/o": "",
	} {
		id := Identify(gh, remote)
		if id.Mine != (want != "") || id.Repository != want {
			t.Errorf("%s: %+v", remote, id)
		}
	}

	ghes := Identify(Config{API: "https://git.example.test/api/v3"}, "https://git.example.test/team/app.git")
	if !ghes.Mine || ghes.Repository != "team/app" || ghes.HeadRef != "refs/pull/{id}/head" || ghes.Web != "https://git.example.test/team/app" {
		t.Fatalf("%+v", ghes)
	}
}

func TestLinks(t *testing.T) {
	w := Links(Config{Remote: "git@github.com:o/r.git"}, plugin.LinksArgs{Commit: headSHA, File: "a/b.go", Change: "pr-7"})
	if w.Commit != "https://github.com/o/r/commit/"+headSHA || w.File != "https://github.com/o/r/blob/"+headSHA+"/a/b.go" || w.Change != "https://github.com/o/r/pull/7" {
		t.Fatalf("%+v", w)
	}
}

func TestAnUnchangedCommentIsNotWritten(t *testing.T) {
	f := newFakeGitHub(t)

	drive(t, f, plugin.CallComment, plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m --> newer"})

	var writes []string

	for _, s := range f.take() {
		if s.Method != http.MethodGet {
			writes = append(writes, s.Method+" "+s.Path)
		}
	}

	if len(writes) != 1 || writes[0] != "DELETE /repos/o/r/issues/comments/41" {
		t.Fatalf("writes: %v", writes)
	}
}

func TestStatusesOfAnUnknownCommit(t *testing.T) {
	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) //nolint:errcheck // nothing to read

		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	t.Cleanup(f.Close)

	var a plugin.StatusesAnswer
	if err := json.Unmarshal(drive(t, f, plugin.CallStatuses, plugin.StatusesArgs{Commit: headSHA}), &a); err != nil || len(a.Statuses) != 0 {
		t.Fatalf("%+v %v", a, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
