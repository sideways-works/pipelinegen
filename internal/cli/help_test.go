package cli

import (
	"strings"
	"testing"
)

func TestEveryCommandDescribesItselfOnHelp(t *testing.T) {
	t.Parallel()

	for _, d := range Commands() {
		for _, spelled := range helpFlags {
			stdout, stderr, code := run(t, d.Name, spelled)
			if code != 0 || stderr != "" {
				t.Fatalf("%s %s: exit %d, stderr %q", d.Name, spelled, code, stderr)
			}

			if !strings.HasPrefix(stdout, "usage: pipelinegen "+d.Name+" ") || !strings.Contains(stdout, d.Summary) {
				t.Fatalf("%s %s: no usage line or summary:\n%s", d.Name, spelled, stdout)
			}

			flags := d.Flags
			for _, s := range d.Subcommands {
				flags = append(flags, s.Flags...)

				if !strings.Contains(stdout, "pipelinegen "+d.Name+" "+s.Name+": ") {
					t.Fatalf("%s %s: subcommand %s missing:\n%s", d.Name, spelled, s.Name, stdout)
				}
			}

			for _, f := range flags {
				if !strings.Contains(stdout, "--"+f.Name) {
					t.Fatalf("%s %s: flag --%s missing:\n%s", d.Name, spelled, f.Name, stdout)
				}
			}
		}
	}
}

func TestHelpAfterASubcommandOrFlags(t *testing.T) {
	t.Parallel()

	stdout, _, code := run(t, "lists", "attach", "--root", t.TempDir(), "--help")
	if code != 0 || !strings.HasPrefix(stdout, "usage: pipelinegen lists <subcommand>") {
		t.Fatalf("lists attach --help: exit %d\n%s", code, stdout)
	}

	// after "--" a help flag is an argument, not a question.
	if _, _, code := run(t, "version", "--", "--help"); code != 2 {
		t.Fatalf("version -- --help: exit %d, want 2", code)
	}
}

func TestHelpListsTheCommands(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		stdout, _, code := run(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}

		for _, c := range commands() {
			if !strings.Contains(stdout, "  "+c.name+" ") {
				t.Fatalf("%v: command %s missing:\n%s", args, c.name, stdout)
			}
		}
	}

	if _, stderr, code := run(t); code != 2 || !strings.Contains(stderr, "pipelinegen <command> --help") {
		t.Fatalf("no args: exit %d\n%s", code, stderr)
	}

	stdout, _, code := run(t, "help", "affected")
	if code != 0 || !strings.HasPrefix(stdout, "usage: pipelinegen affected [flags]") || !strings.Contains(stdout, "--base <value>") {
		t.Fatalf("help affected: exit %d\n%s", code, stdout)
	}

	if _, _, code := run(t, "help", "nope"); code != 2 {
		t.Fatalf("help nope: exit %d, want 2", code)
	}
}
