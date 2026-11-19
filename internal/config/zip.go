package config

import (
	"errors"
	"fmt"
	"net/url"
)

// Zip is a plugin built and packed apart, pinned by where its zip lies and the zip's SHA-256: the
// zip holds manifest.yaml, which names the plugin, its kind and its program, at its top level.
type Zip struct {
	URL    string `json:"url"    yaml:"url"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

const schemeHTTPS = "https"

var errZip = errors.New("an https url and the zip's sha256, 64 lower-case hex digits")

func (z Zip) check() error {
	u, err := url.Parse(z.URL)
	if err != nil || u.Scheme != schemeHTTPS || u.Host == "" || !sha256Hex.MatchString(z.SHA256) {
		return fmt.Errorf("%w (url %q, sha256 %q)", errZip, z.URL, z.SHA256)
	}

	return nil
}

// pinProblem says what is wrong with the entry's pin: exactly one of a release, a commit and a zip,
// a commit's with the repository it is of.
func (p Plugin) pinProblem() string {
	switch {
	case p.Zip != nil && (p.Release != nil || p.Commit != ""):
		return "needs exactly one of release, commit or zip"
	case p.Zip != nil:
		if err := p.Zip.check(); err != nil {
			return "zip: " + err.Error()
		}
	case p.Repository == "":
		return "repository is missing"
	case (p.Release == nil) == (p.Commit == "") || (p.Release != nil && !p.Release.valid()) || (p.Commit != "" && !commitID.MatchString(p.Commit)):
		return "needs exactly one of release (version and one of sha256 or sums), commit (40 hex) or zip (url and sha256)"
	}

	return ""
}
