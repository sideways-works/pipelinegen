// Package gitssh has git reach an ssh remote in a pipeline's step: with
// the private key of the git operation's purpose from the step's
// environment, written to a file only this user can read in a directory
// the tool removes again, and the host keys the config lists, checked
// strictly. A host key that isn't listed fails the fetch: never accepted on
// first use, never skipped.
package gitssh

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// KeyEnv is the variable a step finds the private key of a purpose in
// (PIPELINEGEN_SSH_KEY_READ, PIPELINEGEN_SSH_KEY_STORE), put there by the
// runner from the var git.ssh_key names for it.
func KeyEnv(purpose string) string { return "PIPELINEGEN_SSH_KEY_" + strings.ToUpper(purpose) }

// Purposes are the purposes an ssh key serves: fetching the code, and
// writing the records.
var Purposes = []string{plugin.PurposeRead, plugin.PurposeStore}

// ErrSSH is an ssh setup that can't be made.
var ErrSSH = errors.New("ssh")

// IsSSH tells whether git reaches a remote over ssh: an ssh:// URL, or
// the scp form (git@host:owner/name).
func IsSSH(remote string) bool {
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" {
		return u.Scheme == "ssh" || u.Scheme == "git+ssh" || u.Scheme == "ssh+git"
	}

	at := strings.Index(remote, ":")

	return at > 0 && !strings.Contains(remote[:at], "/") && !strings.Contains(remote, "://")
}

// Valid tells whether an ssh remote names a host and a path, and no
// password.
func Valid(remote string) bool {
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" {
		_, hasPassword := u.User.Password()

		return IsSSH(remote) && u.Host != "" && u.Path != "" && u.Path != "/" && !hasPassword && u.RawQuery == "" && u.Fragment == ""
	}

	host, path, ok := strings.Cut(remote, ":")

	return IsSSH(remote) && ok && host != "" && path != "" && !strings.ContainsAny(remote, " \t\n")
}

// KnownHostsProblem is what is wrong with a known_hosts line, empty when
// nothing is: hosts, a key type and a key, after an optional marker.
func KnownHostsProblem(line string) string {
	f := strings.Fields(line)
	if len(f) > 0 && strings.HasPrefix(f[0], "@") {
		f = f[1:]
	}

	switch {
	case strings.ContainsAny(line, "\n\r"):
		return "a known_hosts entry is one line"
	case len(f) < 3:
		return fmt.Sprintf("%q: a known_hosts line is hosts, a key type and a key", line)
	}

	return ""
}

// Keys hands out, per purpose, the ssh command git runs with: the purpose's
// key from the environment, written once into a directory made for this
// process, which Close, an interrupt or a termination removes.
type Keys struct {
	KnownHosts []string

	mu       sync.Mutex
	dir      string
	commands map[string]string
	stop     chan os.Signal
}

// Env is the environment git needs for the purpose's key: GIT_SSH_COMMAND,
// or nothing when the step's environment holds no key for it, so git runs
// with the person's own ssh setup.
func (k *Keys) Env(purpose string) ([]string, error) {
	key := os.Getenv(KeyEnv(purpose))
	if key == "" {
		return nil, nil
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	if c, ok := k.commands[purpose]; ok {
		return []string{"GIT_SSH_COMMAND=" + c}, nil
	}

	if err := k.makeDir(); err != nil {
		return nil, err
	}

	dir := filepath.Join(k.dir, purpose)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSSH, err)
	}

	c, err := Command(dir, key, k.KnownHosts)
	if err != nil {
		return nil, err
	}

	if k.commands == nil {
		k.commands = map[string]string{}
	}

	k.commands[purpose] = c

	return []string{"GIT_SSH_COMMAND=" + c}, nil
}

// Dir is the directory the keys are in, "" before the first; for tests.
func (k *Keys) Dir() string {
	k.mu.Lock()
	defer k.mu.Unlock()

	return k.dir
}

// makeDir makes the keys' directory, readable by this user only, outside
// any work tree, and removes it again if the process is interrupted or
// terminated.
func (k *Keys) makeDir() error {
	if k.dir != "" {
		return nil
	}

	dir, err := os.MkdirTemp("", "pipelinegen-ssh-")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSSH, err)
	}

	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory: its owner must enter it, and only its owner
		_ = os.RemoveAll(dir) //nolint:errcheck // already failing

		return fmt.Errorf("%w: %w", ErrSSH, err)
	}

	// the path names the key's file: a message that quotes it shows none
	secret.Add(dir)

	k.dir = dir
	k.stop = make(chan os.Signal, 1)
	signal.Notify(k.stop, os.Interrupt, syscall.SIGTERM)

	go func(stop chan os.Signal, dir string) {
		sig, ok := <-stop
		if !ok {
			return
		}

		_ = os.RemoveAll(dir) //nolint:errcheck // the process ends either way

		// end as the signal would have ended the process
		signal.Reset(sig)

		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(sig) //nolint:errcheck // the process ends either way
		}
	}(k.stop, dir)

	return nil
}

// Close removes the keys' directory and stops watching for signals.
func (k *Keys) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.dir == "" {
		return nil
	}

	signal.Stop(k.stop)
	close(k.stop)

	err := os.RemoveAll(k.dir)
	k.dir, k.commands = "", nil

	if err != nil {
		return fmt.Errorf("%w: %w", ErrSSH, err)
	}

	return nil
}

// Command writes key and the known hosts into dir, readable by this user
// only, and returns what GIT_SSH_COMMAND takes: ssh with that key alone,
// no agent and no password, those hosts alone, strict checking, no prompt
// and none of the person's ssh config.
func Command(dir, key string, knownHosts []string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("%w: no private key", ErrSSH)
	}

	if len(knownHosts) == 0 {
		return "", fmt.Errorf("%w: no known hosts: git.known_hosts lists the server's host keys", ErrSSH)
	}

	secret.Add(key)

	keyFile, hostsFile := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")

	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}

	if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil { //nolint:gosec // dir is the keys' own directory, made here
		return "", fmt.Errorf("%w: %w", ErrSSH, err)
	}

	if err := os.WriteFile(hostsFile, []byte(strings.Join(knownHosts, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("%w: %w", ErrSSH, err)
	}

	return strings.Join([]string{
		"ssh", "-F", "/dev/null", "-i", quote(keyFile),
		"-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "ForwardAgent=no",
		"-o", "PasswordAuthentication=no", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + quote(hostsFile),
		"-o", "GlobalKnownHostsFile=/dev/null",
	}, " "), nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
