package plaingit

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func server(t *testing.T) (string, map[string]string) {
	t.Helper()

	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "r.git")
	git(t, work, "init", "-q", "-b", "main")
	git(t, work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one")
	git(t, work, "init", "-q", "--bare", bare)

	heads := map[string]string{}

	for _, b := range []string{"review/a", "review/b", "review/team/c", "personal/wip"} {
		git(t, work, "checkout", "-q", "-b", b, "main")
		git(t, work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", b)
		heads[b] = git(t, work, "rev-parse", "HEAD")
	}

	heads["main"] = git(t, work, "rev-parse", "main")
	git(t, work, "push", "-q", bare, "--all")

	return "file://" + bare, heads
}

func TestChangesAreMatchingBranches(t *testing.T) {
	remote, heads := server(t)

	got, err := Changes(t.Context(), remote, nil, "main", []string{"review/*", "main"})
	if err != nil {
		t.Fatal(err)
	}

	want := []plugin.Change{
		{ID: "branch-review%2Fa", Base: "main", Head: heads["review/a"], HeadBranch: "review/a", From: plugin.FromSame},
		{ID: "branch-review%2Fb", Base: "main", Head: heads["review/b"], HeadBranch: "review/b", From: plugin.FromSame},
	}

	if len(got) != len(want) {
		t.Fatalf("changes: %+v", got)
	}

	for i := range want {
		if got[i].ID != want[i].ID || got[i].Head != want[i].Head || got[i].HeadBranch != want[i].HeadBranch || got[i].Base != "main" || got[i].From != plugin.FromSame {
			t.Errorf("change %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	nested, err := Changes(t.Context(), remote, nil, "main", []string{"review/*/*"})
	if err != nil || len(nested) != 1 || nested[0].HeadBranch != "review/team/c" {
		t.Fatalf("nested: %+v %v", nested, err)
	}
}

func TestAnUnreachableRemoteIsAnError(t *testing.T) {
	_, err := Changes(t.Context(), "file://"+filepath.Join(t.TempDir(), "none.git"), nil, "main", []string{"*"})
	if !errors.Is(err, ErrList) {
		t.Fatalf("err = %v", err)
	}
}

func TestDescribe(t *testing.T) {
	d := Describe()
	if d.Kind != plugin.KindConnector || len(d.Capabilities) != 1 || d.Capabilities[0] != plugin.CapChanges {
		t.Fatalf("%+v", d)
	}
}

func TestLinks(t *testing.T) {
	w := Links("https://cgit.example.test/r/tree/{path}?id={commit}&h={branch}", plugin.LinksArgs{Commit: "abc", File: "a/b.go"}, "review/a")
	if w.Commit != "https://cgit.example.test/r/tree/?id=abc&h=review/a" || w.File != "https://cgit.example.test/r/tree/a/b.go?id=abc&h=review/a" {
		t.Fatalf("%+v", w)
	}

	if (Links("", plugin.LinksArgs{Commit: "abc"}, "")) != (plugin.WebLinks{}) {
		t.Fatal("links without a template")
	}
}
