package plugins

import (
	"maps"
	"slices"
	"unicode"
)

// collidingPaths are the groups of tracked paths a case-folding or
// Unicode-normalising filesystem (a Mac's, by default) may hold as one file.
// Without Unicode's tables the key is conservative: such a pair is planned
// wide, never missed.
func collidingPaths(paths []string) [][]string {
	byKey := map[string][]string{}

	for _, p := range paths {
		k := foldKey(p)
		byKey[k] = append(byKey[k], p)
	}

	var out [][]string

	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		if group := byKey[k]; len(group) > 1 {
			out = append(out, slices.Sorted(slices.Values(group)))
		}
	}

	return out
}

func foldKey(p string) string {
	const unknownLetter = '\uFFFD'

	var out []rune

	for _, r := range p {
		last := len(out) - 1

		switch {
		case unicode.Is(unicode.M, r):
			// a mark composes with the letter before it
			if last >= 0 && unicode.IsLetter(out[last]) {
				out[last] = unknownLetter
			} else if last < 0 || out[last] != unknownLetter {
				out = append(out, unknownLetter)
			}
		case r >= 0x80 && unicode.IsLetter(r):
			if last < 0 || out[last] != unknownLetter {
				out = append(out, unknownLetter)
			}
		default:
			out = append(out, unicode.ToLower(r))
		}
	}

	return string(out)
}
