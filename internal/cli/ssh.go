package cli

import (
	"os"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"
)

// sshKeys are removed when the command ends (closeSSH), or when the process is interrupted or terminated.
var sshKeys struct {
	sync.Mutex

	keys *gitssh.Keys
}

// sshEnv is nothing where the step's environment holds no key: a person's machine uses their own ssh setup.
func sshEnv(cfg *config.Config, purpose string) []string {
	if !cfg.UsesSSH() {
		return nil
	}

	sshKeys.Lock()
	if sshKeys.keys == nil {
		sshKeys.keys = &gitssh.Keys{KnownHosts: cfg.Git.KnownHosts}
	}

	k := sshKeys.keys
	sshKeys.Unlock()

	env, err := k.Env(purpose)
	if err != nil {
		// git then fails on the remote, with ssh's own message.
		warnf(os.Stderr, "pipelinegen: %v\n", err)

		return nil
	}

	return env
}

func closeSSH() {
	sshKeys.Lock()
	defer sshKeys.Unlock()

	if sshKeys.keys != nil {
		_ = sshKeys.keys.Close() //nolint:errcheck // the temporary directory's, if it stays
		sshKeys.keys = nil
	}
}
