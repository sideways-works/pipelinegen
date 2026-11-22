package release

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The licences the family's code is under, as a NOTICE names them. The AGPL's text is version 3's,
// and the family's LICENSE takes any later version too, as the text's own appendix has it.
const (
	apacheLicence = "the Apache License, Version 2.0"
	agplLicence   = "the GNU Affero General Public License, version 3 or (at your option) any later version"
)

// licenceText names the licence a LICENSE file holds by its title, in its first lines.
func licenceText(path string) (string, bool) {
	f, err := os.Open(path) //nolint:gosec // a LICENSE of the family's own code
	if err != nil {
		return "", false
	}
	defer f.Close() //nolint:errcheck // read only

	sc := bufio.NewScanner(f)
	for i := 0; i < 8 && sc.Scan(); i++ {
		line := strings.Join(strings.Fields(sc.Text()), " ")

		switch {
		case strings.HasPrefix(line, "GNU AFFERO GENERAL PUBLIC LICENSE"):
			return agplLicence, true
		case line == "Apache License":
			return apacheLicence, true
		}
	}

	return "", false
}

// licenceOf names the licence of the code in dir: the nearest LICENSE in it or above it, up to stop
// (a module's root), or with stop empty up to the repository's root, the directory with .git.
func licenceOf(dir, stop string) (string, error) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if p := filepath.Join(d, licenseFile); exists(p) {
			if l, ok := licenceText(p); ok {
				return l, nil
			}

			return "", refuseNotice("%s is neither the Apache License 2.0 nor the GNU AGPL 3.0", p)
		}

		if d == filepath.Clean(stop) || (stop == "" && exists(filepath.Join(d, ".git"))) || filepath.Dir(d) == d {
			return "", refuseNotice("%s has no LICENSE in it or above it", dir)
		}
	}
}

// ownCode is the family's code a release includes besides its own: a repository, or a directory of
// the release's own module under a licence of its own.
type ownCode struct{ name, licence string }

// ownNames are the names of own's code, sorted and each once, joined as a list is in prose.
func ownNames(own []ownCode) string {
	names := namesOf(own)

	listed := strings.Join(names, ", ")
	if i := strings.LastIndex(listed, ", "); i >= 0 {
		listed = listed[:i] + " and " + listed[i+2:]
	}

	return listed
}

func compactOwn(own []ownCode) []ownCode {
	slices.SortFunc(own, func(a, b ownCode) int { return strings.Compare(a.licence+"\x00"+a.name, b.licence+"\x00"+b.name) })

	return slices.Compact(own)
}

// ownClause says of the family's code the release includes which licence each part has: the
// release's own first, then each other licence.
func ownClause(licence string, own []ownCode) string {
	byLicence := map[string][]ownCode{}
	for _, o := range own {
		byLicence[o.licence] = append(byLicence[o.licence], o)
	}

	var clauses []string

	if same := byLicence[licence]; len(same) > 0 {
		clauses = append(clauses, ownNames(same)+", which "+have(same)+" the same copyright holder and licence")
	}

	for _, l := range slices.Sorted(func(yield func(string) bool) {
		for l := range byLicence {
			if l != licence && !yield(l) {
				return
			}
		}
	}) {
		other := byLicence[l]

		is := "are"
		if len(namesOf(other)) == 1 {
			is = "is"
		}

		clauses = append(clauses, ownNames(other)+", which "+have(other)+" the same copyright holder and "+is+" licensed under "+l)
	}

	return strings.Join(clauses, ", ")
}

func namesOf(own []ownCode) []string {
	names := make([]string, 0, len(own))
	for _, o := range own {
		names = append(names, o.name)
	}

	slices.Sort(names)

	return slices.Compact(names)
}

func have(own []ownCode) string {
	if len(namesOf(own)) == 1 {
		return "has"
	}

	return "have"
}
