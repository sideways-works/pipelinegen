package plugin

import (
	"fmt"
	"io"

	pk "github.com/sideways-works/plugkit/plugin"
)

// Handler answers one call from its request, or returns ErrCantTell.
type Handler = pk.Handler

// Serve is a plugin's main, returning the exit code: unknown calls and failures write to stderr
// and exit 1, ErrCantTell exits ExitCantTell.
func Serve(args []string, stdin io.Reader, stdout, stderr io.Writer, handlers map[string]Handler) int {
	return pk.Serve(args, stdin, stdout, stderr, handlers)
}

// say writes for whoever reads the plugin's stderr; a failed write there leaves nowhere to
// report it.
func say(w io.Writer, msg string) {
	if _, err := fmt.Fprintln(w, msg); err != nil {
		return
	}
}
