package testenv

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func writeAndRun(t *testing.T, n int, write func(ctx context.Context, path string, content []byte) error) int {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for ctx.Err() == nil {
				_ = exec.CommandContext(ctx, "true").Run() //nolint:errcheck // only its forks matter
			}
		})
	}

	dir, failed := t.TempDir(), 0

	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("s%d", i))
		if err := write(ctx, p, []byte("#!/bin/sh\nexit 0\n")); err != nil {
			t.Fatal(err)
		}

		if err := exec.CommandContext(ctx, p).Run(); err != nil {
			failed++
		}
	}

	cancel()
	wg.Wait()

	return failed
}

func TestAScriptWrittenWhileOthersForkRuns(t *testing.T) {
	if n := writeAndRun(t, 100, WriteExecutable); n > 0 {
		t.Fatalf("%d of 100 scripts failed to run", n)
	}
}
