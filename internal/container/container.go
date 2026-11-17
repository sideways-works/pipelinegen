// Package container runs a command in an image with docker or podman, the
// way a CI task runs in its image: the build's directories mounted, the
// task's environment and nothing else of this machine's.
package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNoTool is a machine with neither docker nor podman on its PATH.
var ErrNoTool = errors.New("neither docker nor podman is on PATH")

// ErrImage is an image that can't be pulled or read.
var ErrImage = errors.New("image")

// Tools are the container tools looked for, in order.
var Tools = []string{"docker", "podman"}

// Find is the container tool to run with: name when given, else the first
// of Tools on PATH.
func Find(name string) (string, error) {
	if name != "" {
		p, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("--container-tool %s: %w", name, err)
		}

		return p, nil
	}

	for _, t := range Tools {
		if p, err := exec.LookPath(t); err == nil {
			return p, nil
		}
	}

	return "", ErrNoTool
}

// Mount is a host path seen at At in the container.
type Mount struct {
	Host, At string
	ReadOnly bool
}

// Run is one command in one container, removed when it ends.
type Run struct {
	Tool  string
	Image string
	Name  string
	Dir   string
	// Mounts are bound in order; a path holding a colon can't be bound and is refused by Args.
	Mounts []Mount
	// Env is the container's whole environment besides the image's own.
	Env map[string]string
	// User and UserNS are --user and --userns, when set.
	User, UserNS string
	Privileged   bool
	// Keep leaves the container once it ends, for a commit; whoever keeps it removes it.
	Keep bool
	// Before are shell lines run in the same container ahead of Cmd, stopping at the first that
	// fails: where installing what a suite declares goes. After are run once Cmd ends, whatever
	// its status, which the container then exits with. Either needs a shell in the image.
	Before, After []string
	Cmd           []string
}

var errColon = errors.New("a path with a colon can't be mounted")

// Args are the tool's arguments: run, with the default bridge network.
func (r Run) Args() ([]string, error) {
	args := []string{"run", "--rm", "--name", r.Name, "--network", "bridge", "--workdir", r.Dir}
	if r.Keep {
		args = slices.Delete(args, 1, 2)
	}

	if r.Privileged {
		args = append(args, "--privileged")
	}

	if r.User != "" {
		args = append(args, "--user", r.User)
	}

	if r.UserNS != "" {
		args = append(args, "--userns", r.UserNS)
	}

	for _, m := range r.Mounts {
		if strings.Contains(m.Host, ":") || strings.Contains(m.At, ":") {
			return nil, fmt.Errorf("%w: %s", errColon, m.Host)
		}

		v := m.Host + ":" + m.At
		if m.ReadOnly {
			v += ":ro"
		}

		args = append(args, "--volume", v)
	}

	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	for _, k := range keys {
		args = append(args, "--env", k+"="+r.Env[k])
	}

	// the command runs as given, as a CI task's does, whatever entrypoint the image names
	cmd := r.Cmd
	if len(r.Before) > 0 || len(r.After) > 0 {
		script := "set -e\n" + strings.Join(r.Before, "\n") + "\nset +e\n\"$@\"\nrc=$?\n" + strings.Join(r.After, "\n") + "\nexit $rc\n"
		cmd = append([]string{"sh", "-c", script, "sh"}, r.Cmd...)
	}

	if len(cmd) == 0 {
		return nil, errNoCommand
	}

	return append(append(args, "--entrypoint", cmd[0], r.Image), cmd[1:]...), nil
}

var errNoCommand = errors.New("a container needs a command")

// Command is the tool running r. Stopping ctx kills the container itself:
// stopping only the tool's process would leave it running.
func (r Run) Command(ctx context.Context) (*exec.Cmd, error) {
	args, err := r.Args()
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, r.Tool, args...) //nolint:gosec // docker or podman, running the suite's own steps
	cmd.Cancel = func() error {
		kill := exec.CommandContext(context.WithoutCancel(ctx), r.Tool, "kill", r.Name) //nolint:gosec // the container this run started
		_ = kill.Run()                                                                  //nolint:errcheck // it may have ended already

		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 30 * time.Second

	return cmd, nil
}

// Name is a container's name for a run of what: unique on this machine.
func Name(what string) string {
	return "pipelinegen-" + what + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36)
}

// Ensure pulls ref unless the tool has it already, and returns its
// platform, as os/arch.
func Ensure(ctx context.Context, tool, ref string, log io.Writer) (string, error) {
	if p, err := platform(ctx, tool, ref); err == nil {
		return p, nil
	}

	pull := exec.CommandContext(ctx, tool, "pull", ref) //nolint:gosec // the image the config names
	pull.Stdout, pull.Stderr = log, log

	if err := pull.Run(); err != nil {
		return "", fmt.Errorf("%w %s: %s pull: %w", ErrImage, ref, tool, err)
	}

	p, err := platform(ctx, tool, ref)
	if err != nil {
		return "", fmt.Errorf("%w %s: %w", ErrImage, ref, err)
	}

	return p, nil
}

func platform(ctx context.Context, tool, ref string) (string, error) {
	var out, errOut bytes.Buffer

	cmd := exec.CommandContext(ctx, tool, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", ref) //nolint:gosec // the image the config names
	cmd.Stdout, cmd.Stderr = &out, &errOut

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s image inspect: %w: %s", tool, err, strings.TrimSpace(errOut.String()))
	}

	p := strings.TrimSpace(out.String())
	if strings.Count(p, "/") != 1 {
		return "", fmt.Errorf("%w: %s image inspect says its platform is %q", ErrImage, tool, p)
	}

	return p, nil
}

// Owner says how what a container writes comes to be this machine's user's. Its processes are root by
// default, as a CI task's are: on a rootful Linux tool what they write is then root's, and handBack
// says to give it back once the container ends; rootless docker or podman maps root to the user
// already. asMe runs them as the user instead: --user on a rootful tool, --userns keep-id with
// rootless podman. Elsewhere than Linux the tool's file sharing gives the user what they write.
func Owner(ctx context.Context, tool string, asMe bool) (user, userns string, handBack bool) {
	if runtime.GOOS != "linux" {
		return "", "", false
	}

	if rootless(ctx, tool) {
		if asMe && isPodman(tool) {
			return "", "keep-id", false
		}

		return "", "", false
	}

	if asMe {
		return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "", false
	}

	return "", "", true
}

// HandBack is the container that gives this machine's user back what r's root wrote under dirs: run
// once r has ended, however it ended, in r's image, which has chown as any image with a shell does.
func HandBack(r Run, dirs ...string) Run {
	mounts := make([]Mount, 0, len(dirs))
	for _, d := range dirs {
		mounts = append(mounts, Mount{Host: d, At: d})
	}

	return Run{
		Tool: r.Tool, Image: r.Image, Name: r.Name + "-chown", Dir: "/", Mounts: mounts,
		Cmd: append([]string{"chown", "-R", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}, dirs...),
	}
}

func isPodman(tool string) bool { return strings.Contains(baseName(tool), "podman") }

func baseName(p string) string {
	if _, after, ok := strings.CutLast(p, "/"); ok {
		return after
	}

	return p
}

func rootless(ctx context.Context, tool string) bool {
	format := "{{.SecurityOptions}}"
	if isPodman(tool) {
		format = "{{.Host.Security.Rootless}}"
	}

	out, err := exec.CommandContext(ctx, tool, "info", "--format", format).Output()
	if err != nil {
		return false
	}

	s := string(out)

	return strings.Contains(s, "rootless") || strings.TrimSpace(s) == "true"
}

// Reachable says whether the tool's daemon or service answers.
func Reachable(ctx context.Context, tool string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	return exec.CommandContext(ctx, tool, "info").Run() == nil
}

// ImageID is the content digest of the image ref the tool has: another
// image under the same tag has another.
func ImageID(ctx context.Context, tool, ref string) (string, error) {
	return inspect(ctx, tool, ref, "{{.Id}}")
}

// HasImage tells whether the tool has the image ref.
func HasImage(ctx context.Context, tool, ref string) bool {
	_, err := inspect(ctx, tool, ref, "{{.Id}}")

	return err == nil
}

func inspect(ctx context.Context, tool, ref, format string) (string, error) {
	var out, errOut bytes.Buffer

	cmd := exec.CommandContext(ctx, tool, "image", "inspect", "--format", format, ref) //nolint:gosec // an image the config names, or one pipelinegen made
	cmd.Stdout, cmd.Stderr = &out, &errOut

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w %s: %s image inspect: %w: %s", ErrImage, ref, filepath.Base(tool), err, strings.TrimSpace(errOut.String()))
	}

	return strings.TrimSpace(out.String()), nil
}

// Remove removes the container name, which a run kept, if it is there.
func Remove(ctx context.Context, tool, name string) {
	rm := exec.CommandContext(context.WithoutCancel(ctx), tool, "rm", "-f", name) //nolint:gosec // the container a run kept
	_ = rm.Run()                                                                  //nolint:errcheck // nothing left to remove
}

// Commit makes the container name, which has ended, the image ref, with
// the entrypoint and command of the image base it ran, and removes it.
func Commit(ctx context.Context, tool, name, base, ref string, log io.Writer) error {
	defer Remove(ctx, tool, name)

	args := []string{"commit"}

	for _, field := range []string{"Entrypoint", "Cmd"} {
		v, err := inspect(ctx, tool, base, "{{json .Config."+field+"}}")
		if err != nil {
			return err
		}

		// an empty list keeps the container's own; an empty name is none
		if v == "null" || v == "" || v == "[]" {
			v = `[""]`
			if field == "Cmd" {
				v = "[]"
			}
		}

		args = append(args, "--change", strings.ToUpper(field)+" "+v)
	}

	c := exec.CommandContext(ctx, tool, append(args, name, ref)...) //nolint:gosec // the container this run kept, and the tag pipelinegen gives it
	// stdout is only the new image's ID
	c.Stderr = log

	if err := c.Run(); err != nil {
		return fmt.Errorf("%w %s: %s commit: %w", ErrImage, ref, filepath.Base(tool), err)
	}

	return nil
}
