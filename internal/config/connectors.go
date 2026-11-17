package config

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Connector is a place the code, or its proposed changes, live: for now
// the plain git connector, for a git server with no host API.
type Connector struct {
	Name string `json:"name" yaml:"name"`

	Plugin string `json:"plugin" yaml:"plugin"`
	Remote string `json:"remote" yaml:"remote"`

	Changes ConnectorChanges `json:"changes" yaml:"changes"`

	Links string `json:"links" yaml:"links"`
}

// ConnectorChanges selects the branches a plain git connector offers as
// proposed changes.
type ConnectorChanges struct {
	Branches []string `json:"branches" yaml:"branches"`
}

// The connectors a config uses by name: git under connectors, github for
// the github section.
const (
	ConnectorGit    = "git"
	ConnectorGitHub = "github"
)

// KindConnector is a plugins entry that pins the git or github connector.
const KindConnector = plugin.KindConnector

// ConnectorPin is a connector's release: the repository whose GitHub
// release holds it, its version, and its binaries' checksums by platform,
// or the checksum of the release's SHA256SUMS.
type ConnectorPin struct {
	Name       string    `json:"name"`
	Repository string    `json:"repository"`
	Version    string    `json:"version"`
	SHA256     Checksums `json:"sha256,omitempty"`
	Sums       string    `json:"sums,omitempty"`
	// URL overrides where it is fetched from; %s is replaced by the
	// repository, the version and the file's name, in that order.
	URL string `json:"url,omitempty"`
}

// PinnedConnectors are the connector releases a config uses unless a
// plugins entry of kind connector by the same name pins another.
var PinnedConnectors = map[string]ConnectorPin{
	ConnectorGitHub: {
		Name: ConnectorGitHub, Repository: "sideways-works/pipelinegen-connector-github", Version: "v0.1.0",
		SHA256: Checksums{LinuxAMD64: "94d20cc9a5b3c2f342da32b0f6786d847f05b4973e7126590116ca7bf5c5a72c", LinuxARM64: "75722982d60d0d002b5675dd64362d01a8349c1a421966930ea67afa694bcedc",
			DarwinAMD64: "5ede86132301c8b438efa0a7fc4b3ece5be5464bb928849303e1d60e2bc05cd4", DarwinARM64: "b881a86ce48243abbece72813ca149f0a14d36bc260c191df483e82132d8dbae"},
	},
	ConnectorGit: {
		Name: ConnectorGit, Repository: "sideways-works/pipelinegen-connector-git", Version: "v0.1.0",
		SHA256: Checksums{LinuxAMD64: "5192041fd8508f768ff0a988ebc467490e068c64b1fa64b9beb36da676065e74", LinuxARM64: "47f2def136ad1156ca83416eed2664f654efd0ed246c042b4c190677f712f959",
			DarwinAMD64: "51c1aa9a3ad5d6d6265ce761570540a7701e564b4684461084bc7d4f8074970a", DarwinARM64: "e46908311b4052a59850e6b05950076760126722203b60ae2b59773e3610d93a"},
	},
}

// ConnectorsUsed names the connectors the config runs: github for the
// github section, git for a connector under connectors.
func (c *Config) ConnectorsUsed() []string {
	var out []string

	if c.Reports() {
		out = append(out, ConnectorGitHub)
	}

	if c.ChangesFromConnector() {
		out = append(out, ConnectorGit)
	}

	return out
}

// ConnectorPin is the release of the connector named name the config
// runs: its plugins entry's, or the pinned one.
func (c *Config) ConnectorPin(name string) ConnectorPin {
	for _, p := range c.Plugins {
		if p.Kind != KindConnector || p.Name != name || p.Release == nil {
			continue
		}

		pin := ConnectorPin{Name: name, Repository: p.Repository, Version: p.Release.Version, Sums: p.Release.Sums}
		if p.Release.SHA256 != "" {
			pin.SHA256 = Checksums{anyPlatform: p.Release.SHA256}
		}

		return pin
	}

	return PinnedConnectors[name]
}

var connectorCapabilities = map[string][]string{
	ConnectorGit: {plugin.CapChanges},
}

// CodeConnector is the connector the code and its proposed changes come
// from: the first one; nil when the config names none.
func (c *Config) CodeConnector() *Connector {
	if len(c.Connectors) == 0 {
		return nil
	}

	return &c.Connectors[0]
}

// ChangesFromConnector tells whether runs take their proposed changes from
// a connector rather than from the github section.
func (c *Config) ChangesFromConnector() bool {
	cc := c.CodeConnector()

	return cc != nil && slices.Contains(connectorCapabilities[cc.Plugin], plugin.CapChanges)
}

func (c *Config) validateConnectors(bad func(string, ...any)) {
	seen := map[string]bool{}

	for i, cn := range c.Connectors {
		where := "connectors[" + strconv.Itoa(i) + "]"
		if cn.Name != "" {
			where += " (" + cn.Name + ")"
		}

		switch {
		case !runName.MatchString(cn.Name):
			bad("%s: name must be lower case letters, digits and dashes", where)
		case seen[cn.Name]:
			bad("%s: another connector has this name", where)
		}

		seen[cn.Name] = true

		if cn.Plugin != ConnectorGit {
			bad("%s: plugin %q: connectors takes the git connector in this release; GitHub is the github section", where, cn.Plugin)

			continue
		}

		if cn.Remote != "" && !validRemote(cn.Remote) && !gitssh.IsSSH(cn.Remote) {
			bad("%s: remote %q: an https, http, git or file URL with a path and no credential", where, cn.Remote)
		}

		c.validateGitChanges(where, cn, bad)

		if cn.Links != "" {
			if u, err := url.Parse(strings.NewReplacer("{commit}", "c", "{branch}", "b", "{path}", "p").Replace(cn.Links)); err != nil ||
				(u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				bad("%s: links must be an http(s) URL with {commit}, {branch} or {path} in it", where)
			}
		}
	}

	if len(c.Connectors) > 0 && c.Reports() {
		bad("connectors and the github section both say where changes come from; use one (the GitHub connector replaces the section)")
	}

	c.validateChangeTriggers(bad)
}

// validateGitChanges requires the plain git connector's branch patterns:
// without them every personal branch on the server would run.
func (c *Config) validateGitChanges(where string, cn Connector, bad func(string, ...any)) {
	if len(cn.Changes.Branches) == 0 {
		bad(`%s: changes.branches is required: the branches that are proposed changes, e.g. changes: {branches: ["review/*"]}`, where)
	}

	for _, p := range cn.Changes.Branches {
		if p == "" || strings.HasPrefix(p, "refs/") {
			bad("%s: changes.branches %q: a pattern over branch names, without refs/heads/", where, p)
		}
	}
}

// validateChangeTriggers fails a filter, comment or status the code
// connector has no capability for: the run would otherwise test changes it
// means to skip, or report nowhere.
func (c *Config) validateChangeTriggers(bad func(string, ...any)) {
	cc := c.CodeConnector()
	if cc == nil {
		return
	}

	caps := connectorCapabilities[cc.Plugin]

	for _, name := range c.RunNames() {
		r := c.Runs[name]

		pr := r.Trigger.PullRequests
		if pr == nil {
			continue
		}

		for _, f := range usedFilters(*pr) {
			if need := plugin.FilterCapabilities[f]; !slices.Contains(caps, need) {
				bad("run %s: changes.%s needs a connector that tells %s; %s (%s) can't", name, f, need, cc.Name, cc.Plugin)
			}
		}

		if r.Report.Comment != nil && !slices.Contains(caps, plugin.CapComment) {
			bad("run %s: a comment needs a connector that keeps one; %s (%s) has nowhere to post it", name, cc.Name, cc.Plugin)
		}

		if r.Report.Status != nil && *r.Report.Status && !slices.Contains(caps, plugin.CapStatuses) {
			bad("run %s: report.status needs a connector that takes statuses; %s (%s) has none: `pipelinegen status` reads the run's state", name, cc.Name, cc.Plugin)
		}
	}
}

func usedFilters(pr PullRequests) []string {
	var out []string

	if pr.Draft == DraftsOnly || pr.Draft == DraftsAny {
		out = append(out, "draft")
	}

	if len(pr.Labels) > 0 {
		out = append(out, "labels")
	}

	if len(pr.LabelsWithout) > 0 {
		out = append(out, "labels_without")
	}

	if pr.OncePerReady {
		out = append(out, "once_per_ready")
	}

	return out
}
