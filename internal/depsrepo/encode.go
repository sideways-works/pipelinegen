package depsrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Length bounds of an encoded name: an exact encoding up to maxExact
// bytes, else its first keptPrefix bytes and a hash of the whole.
const (
	maxExact   = 150
	keptPrefix = 100
	hashHex    = 32
)

// Encode turns an id into one path element and target name, injectively: [a-z0-9_-] and a non-leading '.'
// stay, upper case becomes '^' and lower, others %XX; past maxExact, a prefix, '~' and a hash of the id.
func Encode(id string) string {
	var b strings.Builder

	for i := range len(id) {
		c := id[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		case c == '.' && i > 0:
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte('^')
			b.WriteByte(c + 'a' - 'A')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}

	out := b.String()
	if len(out) <= maxExact {
		return out
	}

	sum := sha256.Sum256([]byte(id))

	return out[:keptPrefix] + "~" + hex.EncodeToString(sum[:])[:hashHex]
}
