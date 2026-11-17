package cli

import (
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

const treeDir = "source"

// stepNeeds are what each command needs beyond its flags, which the flags can't tell; each entry names
// the code that makes it so.
var stepNeeds = map[string]toolcalls.Command{
	// runPlan: config from --trigger, plugins from tools/plugins (pluginsDir), Bazel (planWith), plan ref and statuses.
	"plan": {Needs: toolcalls.Needs{Reads: []string{"tools/plugins"}, ConfigFrom: "trigger", Bazel: true, Token: toolcalls.TokenRequired, ReadOnlyWith: "out"}},
	// runTree reads the config from --trigger and the plan ref.
	"tree": {Needs: toolcalls.Needs{ConfigFrom: "trigger", Token: toolcalls.TokenRequired}},
	// runBatches reads the plan and the run's refs.
	"batches": {Needs: toolcalls.Needs{Token: toolcalls.TokenRequired}},
	// Report steps learn their run in step.open (runIDFromEnv) and write refs and statuses.
	"report start":   {Needs: reportNeeds()},
	"report slice":   {Needs: reportNeeds()},
	"report whole":   {Needs: reportNeeds()},
	"report suite":   {Needs: reportNeeds()},
	"report finish":  {Needs: reportNeeds()},
	"report collect": {Needs: reportNeeds()},
	// reportBatchEnd's journal names the tree and the coverage (report.JournalIn: paths relative to the journal).
	"report batch-end": {Needs: toolcalls.Needs{Reads: []string{treeDir}, Recording: []string{"coverage"}, RunFromEnv: true, Token: toolcalls.TokenRequired}},
	// Slice steps write nothing remote without a token, and say so (step.open).
	"slice start":     {Named: true, Needs: sliceNeeds()},
	"slice end":       {Named: true, Needs: sliceNeeds()},
	"slice setup-end": {Needs: sliceNeeds()},
	"slice flush":     {Needs: sliceNeeds()},
	// runPipeline needs Concourse's address (config.EnvConcourseURL when the config leaves it out).
	"pipeline": {Needs: toolcalls.Needs{ConcourseURL: true}},
	// runGitAuth takes the variable's name first.
	"git-auth": {Named: true},
	// runAlsoTo takes pull requests' numbers after its flags.
	"also-to": {Words: true, Needs: toolcalls.Needs{Token: toolcalls.TokenRequired}},
	// runPlugin takes test and the plugin's executable.
	"plugin": {Words: true},
}

func reportNeeds() toolcalls.Needs {
	return toolcalls.Needs{RunFromEnv: true, Token: toolcalls.TokenRequired}
}

func sliceNeeds() toolcalls.Needs {
	return toolcalls.Needs{RunFromEnv: true, Token: toolcalls.TokenOptional}
}

// ToolCommands are the tool's commands as a CI step calls them, with their flags and needs.
func ToolCommands() toolcalls.Commands {
	out := toolcalls.Commands{}

	add := func(name string, flags []FlagDoc) {
		c := stepNeeds[name]
		c.Flags = map[string]toolcalls.Flag{"help": {Bool: true}, "h": {Bool: true}}

		for _, f := range flags {
			c.Flags[f.Name] = toolcalls.Flag{Default: f.Default, Bool: f.Bool, Use: f.Use}
		}

		out[name] = c
	}

	for _, c := range Commands() {
		if len(c.Subcommands) == 0 {
			add(c.Name, c.Flags)
		}

		for _, s := range c.Subcommands {
			add(c.Name+" "+s.Name, s.Flags)
		}
	}

	return out
}
