package storetest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Daemon serves an empty bare repository through git's daemon with stock settings, as a plain git
// server does (no partial fetches, no unnamed commits), and returns its git:// URL.
func Daemon(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	run(t, "git", "init", "--quiet", "--bare", filepath.Join(base, "r.git"))

	var last error

	// the port is free when asked and may be taken before the daemon
	// binds it: a few tries
	for range 3 {
		port, err := freePort(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		url, err := serve(t, base, port)
		if err == nil {
			return url
		}

		last = err
	}

	t.Fatalf("git daemon: %v", last)

	return ""
}

func freePort(ctx context.Context) (int, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("free port: %w", err)
	}

	addr, ok := l.Addr().(*net.TCPAddr)
	if err := l.Close(); err != nil {
		return 0, fmt.Errorf("free port: %w", err)
	}

	if !ok {
		return 0, fmt.Errorf("free port: %w", errNotUp)
	}

	return addr.Port, nil
}

var errNotUp = errors.New("not listening")

func serve(t *testing.T, base string, port int) (string, error) {
	t.Helper()

	daemon, err := daemonPath(t.Context())
	if err != nil {
		return "", err
	}

	// git-daemon itself, not `git daemon`, which would leave it a child of a git process: it dies
	// with the test binary, even one a timeout or a kill ends before its cleanups run
	//nolint:gosec // git's daemon with the test's own arguments
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), daemon, "--reuseaddr", "--listen=127.0.0.1",
		"--port="+strconv.Itoa(port), "--base-path="+base, "--export-all", "--enable=receive-pack", "--informative-errors", base)
	dieWithParent(cmd)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	stop := func() {
		_ = cmd.Process.Kill() //nolint:errcheck // it may have ended already

		<-done
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			return "", fmt.Errorf("ended early: %w", err)
		default:
		}

		if c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr); err == nil {
			_ = c.Close() //nolint:errcheck // only a probe

			t.Cleanup(stop)

			return "git://" + addr + "/r.git", nil
		}
	}

	stop()

	return "", fmt.Errorf("%s: %w", addr, errNotUp)
}

func daemonPath(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--exec-path").Output()
	if err != nil {
		return "", fmt.Errorf("git --exec-path: %w", err)
	}

	return filepath.Join(strings.TrimSpace(string(out)), "git-daemon"), nil
}
