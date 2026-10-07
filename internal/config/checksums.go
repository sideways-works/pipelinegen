package config

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"

	"go.yaml.in/yaml/v3"
)

// Checksums are a release's SHA-256 by platform (linux-amd64,
// darwin-arm64).
type Checksums map[string]string

const anyPlatform = "*"

var (
	platformName = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)
	errChecksums = errors.New("a checksum, or one per platform")
)

// UnmarshalYAML takes one checksum, for whichever platform runs it, or a
// mapping by platform.
func (c *Checksums) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = Checksums{anyPlatform: n.Value}

		return nil
	}

	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("sha256: %w", errChecksums)
	}

	m := map[string]string{}
	if err := n.Decode(&m); err != nil {
		return fmt.Errorf("sha256: %w", err)
	}

	*c = m

	return nil
}

// MarshalYAML writes one checksum back as the string it was.
func (c Checksums) MarshalYAML() (any, error) {
	if sum, ok := c[anyPlatform]; ok && len(c) == 1 {
		return sum, nil
	}

	return map[string]string(c), nil
}

// Platform is the platform this program runs on, as checksums name it.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// For is the checksum for a platform, if the release has one.
func (c Checksums) For(platform string) (string, bool) {
	if sum, ok := c[platform]; ok {
		return sum, true
	}

	sum, ok := c[anyPlatform]

	return sum, ok
}
