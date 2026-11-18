package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var errEmptyPattern = errors.New("empty pattern")

// MatchGlob matches a slash-separated path: "*" and path.Match's other forms stay inside one
// element, "**" stands for any number of them, and a pattern ending in "/" matches all below.
func MatchGlob(pattern, name string) bool {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}

	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			for i := 0; i <= len(name); i++ {
				if matchParts(rest, name[i:]) {
					return true
				}
			}

			return false
		}

		if len(name) == 0 {
			return false
		}

		ok, err := path.Match(pattern[0], name[0])
		if err != nil || !ok {
			return false
		}

		pattern, name = pattern[1:], name[1:]
	}

	return len(name) == 0
}

func checkGlob(pattern string) error {
	if pattern == "" {
		return errEmptyPattern
	}

	for el := range strings.SplitSeq(pattern, "/") {
		if el == "**" {
			continue
		}

		if _, err := path.Match(el, ""); err != nil {
			return fmt.Errorf("bad pattern: %w", err)
		}
	}

	return nil
}
