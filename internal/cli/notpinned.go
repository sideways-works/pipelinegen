package cli

import (
	"io"
	"os"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// A plugin built on this machine answers for the pinned one, which is what --plugins is for; its
// answers are cached apart.
type notPinnedSayer struct {
	w io.Writer

	mu   sync.Mutex
	said map[string]bool
}

var notPinned = &notPinnedSayer{w: os.Stderr}

func (n *notPinnedSayer) say(set *plugins.Set) {
	if set == nil {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	for _, np := range set.NotPinned() {
		key := np.Plugin + "\x00" + np.Found
		if n.said[key] {
			continue
		}

		if n.said == nil {
			n.said = map[string]bool{}
		}

		n.said[key] = true

		warnf(n.w, "pipelinegen: %s\n", np.Line())
	}
}
