package cli

import (
	"flag"

	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

// pathValue is a string flag naming a path and what the command does with it, so the kit can check that
// a step mounts what a command reads and keeps what it writes.
type pathValue struct {
	p   *string
	use toolcalls.Use
}

func (v *pathValue) String() string {
	if v.p == nil {
		return ""
	}

	return *v.p
}

func (v *pathValue) Set(s string) error {
	*v.p = s

	return nil
}

func pathVar(fs *flag.FlagSet, p *string, name, value, usage string, use toolcalls.Use) {
	*p = value
	fs.Var(&pathValue{p: p, use: use}, name, usage)
}

type textValue struct{ p *string }

func (v *textValue) String() string {
	if v.p == nil {
		return ""
	}

	return *v.p
}

func (v *textValue) Set(s string) error {
	*v.p = s

	return nil
}

func textVar(fs *flag.FlagSet, p *string, name, value, usage string) {
	*p = value
	fs.Var(&textValue{p: p}, name, usage)
}

const (
	reads     = toolcalls.Reads
	writes    = toolcalls.Writes
	readsFile = toolcalls.Reads | toolcalls.File
	// writesFile is a file the command writes: in the step's own
	// directory it needs no output.
	writesFile = toolcalls.Writes | toolcalls.File
)
