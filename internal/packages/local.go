package packages

import (
	"context"
	"fmt"
	"io"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/container"
)

// LocalRepository is where the local image store keeps the cached images, each tagged by its key:
// no registry is needed on one machine.
const LocalRepository = "pipelinegen-cache"

// Local is base with ps installed in tool's local store, committed from a root container the first
// time and reused after; keyed by base's image ID, so a base pulled anew gets its own.
func Local(ctx context.Context, tool, base string, ps config.Packages, log io.Writer) (ref string, built bool, err error) { //nolint:nonamedreturns // the image and whether it was made
	id, err := container.ImageID(ctx, tool, base)
	if err != nil {
		return "", false, err
	}

	ref = LocalRepository + "/" + Key(id, ps)
	if container.HasImage(ctx, tool, ref) {
		return ref, false, nil
	}

	r := container.Run{
		Tool: tool, Image: base, Name: container.Name("packages"), Dir: "/", Keep: true,
		Env: map[string]string{EnvClean: "1"},
		Cmd: []string{"sh", "-c", Script(ps)},
	}

	c, err := r.Command(ctx)
	if err != nil {
		return "", false, err
	}

	c.Stdout, c.Stderr = log, log

	if err := c.Run(); err != nil {
		container.Remove(ctx, tool, r.Name)

		return "", false, fmt.Errorf("%w %s: installing %v: %w", container.ErrImage, base, Names(ps), err)
	}

	if err := container.Commit(ctx, tool, r.Name, base, ref, log); err != nil {
		return "", false, err
	}

	return ref, true, nil
}
