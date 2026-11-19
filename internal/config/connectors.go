package config

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Connector is a place the code, or its proposed changes, live: for now the plain git connector,
// for a git server with no host API.
type Connector struct {
	Name string `json:"name" yaml:"name"`

	Plugin string `json:"plugin" yaml:"plugin"`
	Remote string `json:"remote" yaml:"remote"`

	Changes ConnectorChanges `json:"changes" yaml:"changes"`

	Links string `json:"links" yaml:"links"`
}

// ConnectorChanges selects the branches a plain git connector offers as proposed changes.
type ConnectorChanges struct {
	Branches []string `json:"branches" yaml:"branches"`
}

// The connectors a config uses by name: git under connectors, github for the github section.
const (
	ConnectorGit    = "git"
	ConnectorGitHub = "github"
)

const KindConnector = plugin.KindConnector

// ConnectorPin is a connector's release: the repository whose release holds it, its directory
// there when the repository holds several plugins, its version, and its binaries' checksums by
// platform or the checksum of its SHA256SUMS.
type ConnectorPin struct {
	Name       string    `json:"name"`
	Repository string    `json:"repository"`
	Path       string    `json:"path,omitempty"`
	Version    string    `json:"version"`
	SHA256     Checksums `json:"sha256,omitempty"`
	Sums       string    `json:"sums,omitempty"`
	// URL overrides where it is fetched from; %s is replaced by the repository, the version and
	// the file's name, in that order.
	URL string `json:"url,omitempty"`
}

// PinnedConnectors are the connector releases a config uses unless a plugins entry of kind
// connector by the same name pins another.
var PinnedConnectors = map[string]ConnectorPin{
	ConnectorGitHub: {
		Name: ConnectorGitHub, Repository: pluginsRepository, Path: "connector/github", Version: "v0.4.0",
		SHA256: Checksums{LinuxAMD64: "048f3b3c7852d8ec41f0571187ff94f0502848020a4e5582aeaf21d596622cd1", LinuxARM64: "785ca66d43e76201d759e803fba94102dde518de4d0c10bd55668c75b21da920",
			DarwinAMD64: "3e078f649c7e26e638b8f4726c6375736bd856299242e4257c2db001c9d7f5ed", DarwinARM64: "2983e9247fe6ef94f1b79bc6ac16064a44a486444b8909510000d0f1a472c5ec"},
	},
	ConnectorGit: {
		Name: ConnectorGit, Repository: pluginsRepository, Path: "connector/git", Version: "v0.4.0",
		SHA256: Checksums{LinuxAMD64: "dae7e03d2c84877703339aee2edbbbb127de2359ad3cf514a2645ba9cb135bbf", LinuxARM64: "a87c3d07fa3806776742a1003a7039dd6fcff81f103dd815bc106a53620e5370",
			DarwinAMD64: "56cc93776e79e310f53bc66968ea0a662153cf60658b0e189863bc5007097201", DarwinARM64: "d78d761537f19b54602f1ece2c0073ce607e78d89cb8cef58bfed403ddffdd85"},
	},
}

// pluginsRepository holds the connectors the tool pins, each in a directory of its own.
const pluginsRepository = "https://github.com/sideways-works/pipelinegen-plugins"

// Program names the connector's release files.
func (c ConnectorPin) Program() string { return ProgramOf(c.Repository, c.Path) }

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

// ConnectorPin is the release of the named connector the config runs: its plugins entry's, or
// the pinned one.
func (c *Config) ConnectorPin(name string) ConnectorPin {
	for _, p := range c.Plugins {
		if p.Kind != KindConnector || p.Name != name || p.Release == nil {
			continue
		}

		pin := ConnectorPin{Name: name, Repository: p.Repository, Path: p.Path, Version: p.Release.Version, Sums: p.Release.Sums}
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

// CodeConnector is the connector the code and its proposed changes come from: the first one,
// nil when the config names none.
func (c *Config) CodeConnector() *Connector {
	if len(c.Connectors) == 0 {
		return nil
	}

	return &c.Connectors[0]
}

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
				(u.Scheme != schemeHTTPS && u.Scheme != "http") || u.Host == "" {
				bad("%s: links must be an http(s) URL with {commit}, {branch} or {path} in it", where)
			}
		}
	}

	if len(c.Connectors) > 0 && c.Reports() {
		bad("connectors and the github section both say where changes come from; use one (the GitHub connector replaces the section)")
	}

	c.validateChangeTriggers(bad)
}

// validateGitChanges requires the plain git connector's branch patterns: without them every
// personal branch on the server would run.
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

// validateChangeTriggers fails a filter, comment or status the code connector can't do: the run
// would test changes it means to skip, or report nowhere.
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
