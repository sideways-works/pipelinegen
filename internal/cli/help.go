package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
)

var helpFlags = []string{"-h", "-help", "--help"}

// asksHelp finds a help flag before any "--", wherever it stands, so `lists attach --help` asks too.
func asksHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}

		if slices.Contains(helpFlags, a) {
			return true
		}
	}

	return false
}

func runHelp(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// asked to describe itself, it has no flags to hand over.
	if seen, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet); ok {
		*seen = append(*seen, fs)

		return flag.ErrHelp
	}

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	switch fs.NArg() {
	case 0:
		return say(stdout, "%s", commandList())
	case 1:
		text, ok := commandHelp(ctx, fs.Arg(0))
		if !ok {
			return fmt.Errorf("%w: no command %q", errUsage, fs.Arg(0))
		}

		return say(stdout, "%s", text)
	default:
		return fmt.Errorf("%w: unexpected %q", errUsage, fs.Arg(1))
	}
}

func commandList() string {
	var b strings.Builder

	b.WriteString("usage: pipelinegen <command> [flags]\n\n")

	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-10s %s\n", c.name, c.summary)
	}

	b.WriteString("\nRun `pipelinegen <command> --help` for a command's flags.\n")

	return b.String()
}

func commandHelp(ctx context.Context, name string) (string, bool) {
	docs := commandDocs(ctx)

	i := slices.IndexFunc(docs, func(d CommandDoc) bool { return d.Name == name })
	if i < 0 {
		return "", false
	}

	d := docs[i]

	var b strings.Builder

	if len(d.Subcommands) == 0 {
		fmt.Fprintf(&b, "usage: pipelinegen %s [flags]\n\n%s\n", d.Name, d.Summary)
		writeFlags(&b, d.Flags, "")

		return b.String(), true
	}

	fmt.Fprintf(&b, "usage: pipelinegen %s <subcommand> [flags]\n\n%s\n", d.Name, d.Summary)

	for _, s := range d.Subcommands {
		fmt.Fprintf(&b, "\npipelinegen %s %s: %s\n", d.Name, s.Name, s.Summary)
		writeFlags(&b, s.Flags, "  ")
	}

	return b.String(), true
}

func writeFlags(b *strings.Builder, flags []FlagDoc, indent string) {
	if len(flags) == 0 {
		return
	}

	fmt.Fprintf(b, "\n%sflags:\n", indent)

	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)

	for _, f := range flags {
		spelled := "--" + f.Name
		if !f.Bool {
			spelled += " <value>"
		}

		usage := f.Usage
		if f.Default != "" && (!f.Bool || f.Default != "false") {
			usage += fmt.Sprintf(" (default %s)", f.Default)
		}

		if _, err := fmt.Fprintf(tw, "%s  %s\t%s\n", indent, spelled, usage); err != nil {
			return
		}
	}

	if err := tw.Flush(); err != nil {
		return
	}
}
