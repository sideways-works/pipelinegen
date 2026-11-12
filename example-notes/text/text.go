// Package text splits and tidies the words of a note.
package text

import (
	"strings"
	"unicode"
)

// Words returns the words of s in lower case, without punctuation.
func Words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Title trims a title and caps it at 80 characters.
func Title(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		return string(r[:80])
	}

	return s
}
