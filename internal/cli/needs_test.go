package cli

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

func TestEveryFlagSaysWhetherItNamesAPath(t *testing.T) {
	check := func(name string, flags []FlagDoc) {
		for _, f := range flags {
			if f.Plain {
				t.Errorf("%s -%s: a string flag that doesn't say whether it names a path (pathVar or textVar)", name, f.Name)
			}
		}
	}

	for _, c := range Commands() {
		check(c.Name, c.Flags)

		for _, s := range c.Subcommands {
			check(c.Name+" "+s.Name, s.Flags)
		}
	}

	cmds := ToolCommands()
	for name := range stepNeeds {
		if _, ok := cmds[name]; !ok {
			t.Errorf("needs of %s, which is no command", name)
		}
	}

	for _, c := range []struct {
		cmd, flag, def string
		use            toolcalls.Use
	}{
		{"tree", "out", "source", toolcalls.Writes},
		{"tree", "trigger", "trigger", toolcalls.Reads},
		{"report batch-end", "batches", "batches", toolcalls.Reads},
		{"report collect", "out", "slices", toolcalls.Writes},
		{"pipeline", "out", "", toolcalls.Writes | toolcalls.File},
		{"slice setup-end", "coverage", "coverage", toolcalls.Writes},
	} {
		if f := cmds[c.cmd].Flags[c.flag]; f.Use != c.use || f.Default != c.def {
			t.Errorf("%s -%s: %+v, want use %d default %q", c.cmd, c.flag, f, c.use, c.def)
		}
	}

	if !cmds["slice start"].Named || cmds["plan"].Needs.ConfigFrom != "trigger" || cmds["git-auth"].Needs.Token != toolcalls.NoToken {
		t.Error("the needs didn't reach the commands")
	}
}
