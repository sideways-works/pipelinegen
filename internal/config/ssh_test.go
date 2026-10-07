package config

import (
	"strings"
	"testing"
)

const testHostKey = "git.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItestkey"

var withSSH = strings.Replace(withoutHost, `git: {remote: "git://example.test/r.git"}`,
	`git: {remote: "ssh://git@git.example.test/team/r.git", ssh_key: deploy_key, known_hosts: ["`+testHostKey+`"]}`, 1)

func TestAnSSHRemote(t *testing.T) {
	c, err := Parse([]byte(withSSH))
	if err != nil {
		t.Fatal(err)
	}

	if c.Git.SSHKey.For("read") != "deploy_key" || c.Git.SSHKey.For("store") != "deploy_key" {
		t.Fatalf("one var serves both: %+v", c.Git.SSHKey)
	}

	if err := c.ValidatePipeline(); err != nil {
		t.Fatalf("making the pipeline: %v", err)
	}

	for key, refused := range map[string]bool{"deploy_key": true, "read_key": false} {
		text := strings.NewReplacer(
			"ssh_key: deploy_key", "ssh_key: {read: read_key, store: deploy_key}",
			"suites: {e2e: {params: {PROJECTS: two}}", "suites: {e2e: {params: {PROJECTS: two}, secrets: {batch: {K: "+key+"}}}",
		).Replace(withSSH)

		_, err := Parse([]byte(text))
		if refused != (err != nil) || refused && !strings.Contains(err.Error(), "is given deploy_key, the ssh key that writes the store") {
			t.Errorf("a task given %s: %v", key, err)
		}
	}

	split, err := Parse([]byte(strings.Replace(withSSH, "ssh_key: deploy_key", "ssh_key: {read: read_key, store: write_key}", 1)))
	if err != nil {
		t.Fatal(err)
	}

	if split.Git.SSHKey.For("read") != "read_key" || split.Git.SSHKey.For("store") != "write_key" {
		t.Fatalf("by purpose: %+v", split.Git.SSHKey)
	}
}

func TestAnSSHRemoteIsRefusedWhatItLacks(t *testing.T) {
	cases := map[string][3]string{
		"no key":            {"ssh_key: deploy_key, ", "", "an ssh remote needs git.ssh_key"},
		"no host keys":      {`, known_hosts: ["` + testHostKey + `"]`, "", "an ssh remote needs git.known_hosts"},
		"half a key":        {"ssh_key: deploy_key", "ssh_key: {read: read_key}", "an ssh remote needs git.ssh_key"},
		"changes":           {"ssh_key: deploy_key", "ssh_key: {read: a, store: b, changes: c}", "no API to changes through"},
		"report":            {"ssh_key: deploy_key", "ssh_key: {read: a, store: b, report: c}", "no API to report through"},
		"a key's value":     {"ssh_key: deploy_key", `ssh_key: "-----BEGIN KEY-----"`, "must be the name of a var"},
		"a bad host line":   {testHostKey, "git.example.test", "a known_hosts line is hosts, a key type and a key"},
		"a password":        {"ssh://git@git.example.test", "ssh://git:pw@git.example.test", "no password"},
		"a key without ssh": {"ssh://git@git.example.test/team/r.git", "https://git.example.test/team/r.git", "git.ssh_key and git.known_hosts are for an ssh remote"},
	}

	for name, c := range cases {
		text := strings.Replace(withSSH, c[0], c[1], 1)
		if text == withSSH {
			t.Fatalf("%s: nothing replaced", name)
		}

		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), c[2]) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
