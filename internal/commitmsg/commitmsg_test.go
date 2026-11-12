package commitmsg

import (
	"errors"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// The tests' git sees none of the machine's config.
func init() { testenv.Git() } //nolint:gochecknoinits // before any test runs git

func TestGoodMessagesPass(t *testing.T) {
	for _, msg := range []string{
		"fix(report): a batch's journal names its paths relative to itself",
		"docs: the guide's runs, reasons and recordings follow the code\n",
		"feat(kit): a call the plugin declares and the fixture never runs fails\n\n",
		"refactor: the reporter records only through the correlators",
		"ci: the lint slice checks the new commits' subjects",
		"chore: x",
		"perf(plugins): ask once",
		"build: pin bazel 9.2.0\n# Please enter the commit message for your changes.\n#\n",
		"test: " + strings.Repeat("x", MaxSubject-len("test: ")),
	} {
		if err := Check(msg); err != nil {
			t.Errorf("%q: %v", msg, err)
		}
	}
}

func TestBadMessagesFail(t *testing.T) {
	for _, msg := range []string{
		"",
		"# only a comment\n",
		"docs: a suite's tasks get their kind's secrets; coverage is the correlators'",
		"test: " + strings.Repeat("x", MaxSubject-len("test: ")+1),
		"Fix the build",
		"fixed: the build",
		"feat(Kit): upper-case scope",
		"feat:no space",
		"feat: ",
		" feat: leading space",
		"feat: trailing space ",
		"fix: a bug\n\nbecause of a body",
		"fix: a bug\n\nCo-Authored-By: someone <someone@example.com>",
		"fix: a bug\nSigned-off-by: someone",
	} {
		if err := Check(msg); !errors.Is(err, ErrMessage) {
			t.Errorf("%q passed (%v)", msg, err)
		}
	}
}

func TestOnlyTheBuildsNewCommitsAreChecked(t *testing.T) {
	r := testrepo.New(t)
	r.Commit("an old subject that was never checked and can't be rewritten now")
	r.Git("branch", "-M", "main")

	t.Run("a branch build checks its head alone", func(t *testing.T) {
		r.Commit("fix: a good one")

		if err := run(t, r); err != nil {
			t.Fatal(err)
		}

		r.Commit("Bad one")

		if err := run(t, r); !errors.Is(err, ErrMessage) || !strings.Contains(err.Error(), "Bad one") {
			t.Fatalf("got %v", err)
		}

		r.Commit("fix: the next one is good again")

		if err := run(t, r); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a merged pull request checks its own commits", func(t *testing.T) {
		r.Git("checkout", "-q", "-b", "topic")
		r.Commit("feat: a good one")
		r.Commit("feat: and one whose subject runs on far beyond the seventy-two characters")
		r.Git("checkout", "-q", "main")
		r.Commit("docs: main moved meanwhile")
		r.Git("merge", "-q", "--no-ff", "-m", "Merge the pull request", "topic")

		err := run(t, r)
		if !errors.Is(err, ErrMessage) || !strings.Contains(err.Error(), "seventy-two") ||
			strings.Contains(err.Error(), "a good one") || strings.Contains(err.Error(), "Merge") || strings.Contains(err.Error(), "old subject") {
			t.Fatalf("got %v", err)
		}
	})
}

func run(t *testing.T, r *testrepo.Repo) error {
	t.Helper()

	revs, err := New(t.Context(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}

	return Commits(t.Context(), r.Dir, revs...)
}
