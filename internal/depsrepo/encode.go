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

// Encode turns an id (or a group, or a slice's name) into one path
// element and target name: lower-case letters, digits, '-' and '_' stay,
// a '.' stays where it isn't first, an upper-case letter becomes '^' and
// its lower case (so names differ on case-insensitive file systems too),
// and every other byte becomes %XX. That much is injective. A name longer
// than maxExact keeps its first keptPrefix bytes, then '~' and a hash of
// the id; '~' never appears in an exact name, so a long name can only meet
// another through a hash collision.
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
