package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"

	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/scratch"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
)

var scratchDirs struct {
	sync.Mutex

	dirs scratch.Dirs
}

func closeScratch() {
	scratchDirs.Lock()
	defer scratchDirs.Unlock()

	_ = scratchDirs.dirs.Close() //nolint:errcheck // the temporary directory's, if any stays
}

// The checkout is named by nothing in the hand-off and fetched from the remote the trusted sweep wrote.
// A branch that moved on since is ErrSuperseded.
func handOffCheckout(ctx context.Context, run, trigger string, needsTree bool) (string, error) {
	if !needsTree || !runs.IsHandOff(trigger) {
		return "", nil
	}

	ch, err := runs.ReadRunFile(filepath.Join(trigger, runs.RunFile))
	if err != nil {
		return "", err
	}

	if ch.Run != run || ch.Forge == "" || ch.Remote == "" {
		return "", fmt.Errorf("%w: the hand-off is for run %q, forge %q, remote %q", runs.ErrTrigger, ch.Run, ch.Forge, ch.Remote)
	}

	scratchDirs.Lock()
	dir, err := scratchDirs.dirs.Make("pipelinegen-change")
	scratchDirs.Unlock()

	if err != nil {
		return "", err
	}

	// The read token, by its purpose's own variable (the change's config isn't read yet), goes to the
	// hand-off's remote alone, in git's environment for each fetch.
	auth := runs.Auth{Scope: httpsScope(ch.Remote), Token: os.Getenv(toolcalls.PurposeEnv(plugin.PurposeRead))}

	if gitssh.IsSSH(ch.Remote) {
		env, err := handOffSSH()
		if err != nil {
			return "", err
		}

		auth.Env = env
	}

	if err := runs.CheckoutChange(ctx, dir, ch, auth); err != nil {
		return "", hostKeyProblem(ch.Remote, err)
	}

	return dir, nil
}

var errHandOffSSH = errors.New("the change's ssh remote")

// Host keys come from the config the pipeline was made from, never the change's own. Without them or
// the read key it refuses: no fallback on the machine's ssh setup, no trust on first use.
func handOffSSH() ([]string, error) {
	blob := os.Getenv(envTrusted)
	if blob == "" {
		return nil, fmt.Errorf("%w: the pipeline gives this step no trusted keys (%s), so it knows no host key to check the server by: regenerate the pipeline", errHandOffSSH, envTrusted)
	}

	var t config.Trusted
	if err := json.Unmarshal([]byte(blob), &t); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errUsage, envTrusted, err)
	}

	if len(t.KnownHosts) == 0 {
		return nil, fmt.Errorf("%w: git.known_hosts lists no host key, so the server can't be checked: add the server's key to git.known_hosts", errHandOffSSH)
	}

	if os.Getenv(gitssh.KeyEnv(plugin.PurposeRead)) == "" {
		return nil, fmt.Errorf("%w: no read key in %s", errHandOffSSH, gitssh.KeyEnv(plugin.PurposeRead))
	}

	sshKeys.Lock()
	if sshKeys.keys == nil {
		sshKeys.keys = &gitssh.Keys{KnownHosts: t.KnownHosts}
	}

	k := sshKeys.keys
	sshKeys.Unlock()

	return k.Env(plugin.PurposeRead)
}

// ssh checks the host key strictly and never adds one it hasn't seen.
func hostKeyProblem(remote string, err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "Host key verification failed") && !strings.Contains(msg, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		return err
	}

	host := remote
	if u, perr := url.Parse(remote); perr == nil && u.Hostname() != "" {
		host = u.Hostname()
	} else if _, rest, ok := strings.Cut(remote, "@"); ok {
		host, _, _ = strings.Cut(rest, ":")
	}

	return fmt.Errorf("%w: %s's host key isn't one git.known_hosts lists, or it changed, so the fetch was refused; "+
		"no host is trusted on first use. Add the server's key to git.known_hosts as one known_hosts line "+
		"(ssh-keyscan %s prints it; check it against the fingerprint the server's own administrator gives you): %w",
		errHandOffSSH, host, host, err)
}
