package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var hostName = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// What runGitAuth prints names the variable and never holds the token, so a trace of the eval or of git
// shows nothing secret, and the token goes into no URL and no file.
func runGitAuth(ctx context.Context, args []string, stdout io.Writer) error {
	var host string

	fs := flag.NewFlagSet("git-auth", flag.ContinueOnError)
	textVar(fs, &host, "host", "github.com", "the host git authenticates to")

	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if !envName.MatchString(name) {
		return fmt.Errorf("%w: git-auth <ENV_NAME>: the name of the variable that holds the token", errUsage)
	}

	if !hostName.MatchString(host) {
		return fmt.Errorf("%w: git-auth: %q is no host", errUsage, host)
	}

	return say(stdout, "%s", gitAuthLines(name, host, os.Getenv("GIT_CONFIG_COUNT")))
}

// The helper takes the next of git's environment config entries after those already set.
func gitAuthLines(name, host, count string) string {
	n, err := strconv.Atoi(count)
	if err != nil || n < 0 {
		n = 0
	}

	helper := `!f() { test "$1" = get && echo username=x-access-token && echo "password=${` + name + `}"; }; f`

	return fmt.Sprintf("export GIT_CONFIG_COUNT=%d\nexport GIT_CONFIG_KEY_%d='credential.https://%s.helper'\nexport GIT_CONFIG_VALUE_%d='%s'\nexport GIT_TERMINAL_PROMPT=0\n",
		n+1, n, host, n, helper)
}
