package release

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const sumsFile = "SHA256SUMS"

var errSums = errors.New("checksum differs")

// collation orders file names as the published releases list them: letters
// and digits only, case aside, then the name as it is.
func collation(a, b string) int {
	key := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}

			return -1
		}, s)
	}

	if c := strings.Compare(key(a), key(b)); c != 0 {
		return c
	}

	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}

func fileSum(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a release file
	if err != nil {
		return "", fmt.Errorf("sum: %w", err)
	}

	s := sha256.Sum256(data)

	return hex.EncodeToString(s[:]), nil
}

func writeSums(out string) error {
	entries, err := os.ReadDir(out)
	if err != nil {
		return fmt.Errorf("sums: %w", err)
	}

	var names []string

	for _, e := range entries {
		if e.Name() != sumsFile && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}

	slices.SortFunc(names, collation)

	var text strings.Builder

	for _, n := range names {
		sum, err := fileSum(filepath.Join(out, n))
		if err != nil {
			return err
		}

		fmt.Fprintf(&text, "%s  %s\n", sum, n)
	}

	if err := os.WriteFile(filepath.Join(out, sumsFile), []byte(text.String()), 0o644); err != nil { //nolint:gosec // a release file
		return fmt.Errorf("sums: %w", err)
	}

	return nil
}

func checkSums(out string) error {
	f, err := os.Open(filepath.Join(out, sumsFile)) //nolint:gosec // the build's own sums
	if err != nil {
		return fmt.Errorf("sums: %w", err)
	}

	defer func() { _ = f.Close() }() //nolint:errcheck // read only

	sc := bufio.NewScanner(f)

	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			return fmt.Errorf("%w: %q", errSums, sc.Text())
		}

		got, err := fileSum(filepath.Join(out, strings.TrimPrefix(name, "*")))
		if err != nil || got != sum {
			return fmt.Errorf("%w: %s", errSums, name)
		}
	}

	return sc.Err() //nolint:wrapcheck // a read error of the sums, said as it is
}
