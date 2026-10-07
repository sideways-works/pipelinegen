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
// the built-in plain git connector, for a git server with no host API.
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

// ConnectorGit is the built-in plain git connector.
const ConnectorGit = "git"

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
			bad("%s: plugin %q: only the built-in git connector is in this release", where, cn.Plugin)

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
