package config

import (
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/plugin"
)

// SSHKeys name the CI var of the ssh key per purpose: read fetches the code, store writes the
// records. One var serves both.
type SSHKeys struct {
	Read  string `json:"read"  yaml:"read"`
	Store string `json:"store" yaml:"store"`
}

var errSSHKey = errors.New("git.ssh_key")

// UnmarshalYAML takes one var for both purposes, or a mapping by purpose; changes and report
// mean nothing to a plain git server.
func (k *SSHKeys) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		k.Read, k.Store = n.Value, n.Value

		return nil
	}

	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: the name of a var, or one per purpose (read, store)", errSSHKey)
	}

	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1].Value

		switch key {
		case plugin.PurposeRead:
			k.Read = value
		case plugin.PurposeStore:
			k.Store = value
		case plugin.PurposeChanges, plugin.PurposeReport:
			return fmt.Errorf("%w.%s: a plain git server has no API to %s through; only read and store take a key", errSSHKey, key, key)
		default:
			return fmt.Errorf("%w.%s: no such purpose; read and store take a key", errSSHKey, key)
		}
	}

	return nil
}

func (k SSHKeys) For(purpose string) string {
	switch purpose {
	case plugin.PurposeRead:
		return k.Read
	case plugin.PurposeStore:
		return k.Store
	}

	return ""
}

func (c *Config) UsesSSH() bool { return len(c.sshRemotes()) > 0 }

func (c *Config) sshRemotes() []string {
	var out []string

	if gitssh.IsSSH(c.Git.Remote) {
		out = append(out, c.Git.Remote)
	}

	for _, cn := range c.Connectors {
		if gitssh.IsSSH(cn.Remote) {
			out = append(out, cn.Remote)
		}
	}

	return out
}

// validateSSH checks what an ssh remote needs: a key's var for each purpose and the server's
// host keys, since no host key is accepted on first use.
func (c *Config) validateSSH(bad func(string, ...any)) {
	remotes := c.sshRemotes()
	g := c.Git

	for _, p := range gitssh.Purposes {
		if v := g.SSHKey.For(p); v != "" && !varName.MatchString(v) {
			bad("git.ssh_key.%s must be the name of a var, not %q", p, v)
		}
	}

	for _, line := range g.KnownHosts {
		if p := gitssh.KnownHostsProblem(line); p != "" {
			bad("git.known_hosts: %s", p)
		}
	}

	if len(remotes) == 0 {
		if g.SSHKey != (SSHKeys{}) || len(g.KnownHosts) > 0 {
			bad("git.ssh_key and git.known_hosts are for an ssh remote, and no remote is one")
		}

		return
	}

	for _, r := range remotes {
		if !gitssh.Valid(r) {
			bad("ssh remote %q: a host and a path, and no password", r)
		}
	}

	if slices.ContainsFunc(gitssh.Purposes, func(p string) bool { return g.SSHKey.For(p) == "" }) {
		bad("an ssh remote needs git.ssh_key: the var holding the private key, or one per purpose ({read: ..., store: ...})")
	}

	if len(g.KnownHosts) == 0 {
		bad("an ssh remote needs git.known_hosts: the server's host keys as known_hosts lines (ssh-keyscan prints them; check them out of band), since an unknown host key is never accepted")
	}
}
