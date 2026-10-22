package report

import "regexp"

// GroupOf names the group a slice shows under in the comment, by its
// suite's group pattern (empty for the default).
func GroupOf(pattern, slice string) string {
	var re *regexp.Regexp
	if pattern != "" {
		re, _ = regexp.Compile(pattern) //nolint:errcheck // the config check refuses a pattern that doesn't compile
	}

	return groupOf(re, slice)
}
