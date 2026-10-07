package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Handler answers one call: it reads its request from in and returns the
// answer to write as JSON, or ErrCantTell.
type Handler func(in []byte) (any, error)

// Serve is a plugin's main: it runs the call named by args[0] and returns
// the exit code. Unknown calls and failures write to stderr and exit 1;
// ErrCantTell exits ExitCantTell.
func Serve(args []string, stdin io.Reader, stdout, stderr io.Writer, handlers map[string]Handler) int {
	if len(args) == 0 {
		say(stderr, "a plugin needs the name of a call")

		return 1
	}

	h, ok := handlers[args[0]]
	if !ok {
		say(stderr, fmt.Sprintf("no call %q", args[0]))

		return 1
	}

	in, err := io.ReadAll(stdin)
	if err != nil {
		say(stderr, err.Error())

		return 1
	}

	answer, err := h(in)

	switch {
	case errors.Is(err, ErrCantTell):
		// why it can't tell goes with the answer: the plan's warning
		// carries it
		say(stderr, err.Error())

		return ExitCantTell
	case err != nil:
		say(stderr, err.Error())

		return 1
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		say(stderr, err.Error())

		return 1
	}

	return 0
}

// say writes a message for the person reading the plugin's stderr; a
// failed write there leaves nowhere to report it.
func say(w io.Writer, msg string) {
	if _, err := fmt.Fprintln(w, msg); err != nil {
		return
	}
}
