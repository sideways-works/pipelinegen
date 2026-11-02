package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestUnlockFreesALeaseTheConfigNoLongerNames(t *testing.T) {
	unlockFreesByName(t)
}

func TestUnlockFreesALeaseTheConfigNoLongerNamesOnABranch(t *testing.T) {
	onBranch(t, unlockFreesByName)
}

func unlockFreesByName(t *testing.T) {
	t.Helper()

	root, _, _ := runEnv(t)

	if stdout, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root); code != 0 {
		t.Fatalf("acquire: %d %s %s", code, stdout, stderr)
	}

	shared := filepath.Join(strings.TrimPrefix(os.Getenv(envGitURL), "file://"), "o", "shared.git")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", shared},
		{"-C", shared, "config", "uploadpack.allowFilter", "true"},
		{"-C", shared, "config", "uploadpack.allowAnySHA1InWant", "true"},
	} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil { //nolint:gosec // git on the test's own temporary repository
			t.Fatalf("%v: %s", err, out)
		}
	}

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	take := func(name, repository, holder string) {
		t.Helper()

		l := lockAt(cfg, name, repository)
		l.TTL = time.Hour

		defer closeLock(l, &err)

		if _, err := l.Acquire(t.Context(), holder, "", 0); err != nil {
			t.Fatal(err)
		}
	}

	take("continuous-main", "", "run main main-1 0123456789ab")
	take("stacks", "o/shared", "o/other pr pr-3 0123456789ab e2e")

	dropped := strings.Replace(reportConfig, "    lock: {name: browsers, minutes: 90, slots: 1}\n", "", 1)
	if onBranchStore {
		dropped += "store: {backend: branch}\n"
	}

	writeFile(t, filepath.Join(root, config.FileName), dropped)

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"unlock", "browsers"}, "lock browsers: slot 1 freed, held by o/r pr pr-7 0123456789ab e2e batch 1"},
		{[]string{"unlock", "continuous-main", "1"}, "lock continuous-main: slot 1 freed, held by run main main-1 0123456789ab"},
		{[]string{"unlock", "stacks", "-repository", "o/shared"}, "lock stacks: slot 1 freed, held by o/other pr pr-3 0123456789ab e2e"},
		{[]string{"unlock", "browsers"}, "lock browsers: nothing held"},
	} {
		stdout, stderr, code := run(t, append(c.args, "-root", root)...)
		if code != 0 || !strings.Contains(stdout, c.want) {
			t.Fatalf("%v: %d %q %s, want %q", c.args, code, stdout, stderr, c.want)
		}
	}

	for _, l := range [][2]string{{"browsers", ""}, {"continuous-main", ""}, {"stacks", "o/shared"}} {
		lk := lockAt(cfg, l[0], l[1])

		left, err := lk.Leases(t.Context())
		closeLock(lk, &err)

		if err != nil || len(left) != 0 {
			t.Fatalf("lock %s still has %v (%v)", l[0], left, err)
		}
	}
}
