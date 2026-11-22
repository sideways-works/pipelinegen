package config

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Plans is where each plan a trusted step writes is copied, beside the run's records, and how
// long the copies are kept.
type Plans struct {
	S3 *PlansS3 `json:"s3,omitempty" yaml:"s3"`
	// Keep is how long a copied plan is kept (Nd, Nh or Nm); without it none is deleted.
	Keep Period `json:"keep,omitempty" yaml:"keep"`
}

// PlansS3 is a bucket of an S3-compatible service.
type PlansS3 struct {
	Bucket string `json:"bucket" yaml:"bucket"`
	// Prefix is where in the bucket the plans go, without a slash at either end.
	Prefix string `json:"prefix,omitempty" yaml:"prefix"`
	Region string `json:"region"           yaml:"region"`
	// Endpoint is the service's base URL; left out, AWS's for the region.
	Endpoint  string `json:"endpoint,omitempty"   yaml:"endpoint"`
	PathStyle bool   `json:"path_style,omitempty" yaml:"path_style"`
	// Credentials name the CI vars holding the key, never their values.
	Credentials PlansCredentials `json:"credentials" yaml:"credentials"`
}

type PlansCredentials struct {
	AccessKeyID     string `json:"access_key_id"     yaml:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key" yaml:"secret_access_key"`
}

// EndpointURL is the bucket's service: the endpoint, or AWS's for the region.
func (s PlansS3) EndpointURL() string {
	if s.Endpoint != "" {
		return strings.TrimSuffix(s.Endpoint, "/")
	}

	return "https://s3." + s.Region + ".amazonaws.com"
}

// KeepFor is plans.keep as a duration; false when nothing is to be deleted.
func (p Plans) KeepFor() (time.Duration, bool) {
	if p.Keep == "" || !plansKeepRE.MatchString(string(p.Keep)) {
		return 0, false
	}

	return p.Keep.Duration()
}

const schemeHTTP = "http"

var (
	plansKeepRE = regexp.MustCompile(`^[0-9]+[dhm]$`)
	// a bucket's name as S3 takes it in either style.
	bucketRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	prefixPartRE    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	endpointSchemes = map[string]bool{schemeHTTPS: true, schemeHTTP: true}
)

func (c *Config) validatePlans(bad func(string, ...any)) {
	p := c.Plans

	if p.Keep != "" {
		if d, ok := p.Keep.Duration(); !ok || d <= 0 || !plansKeepRE.MatchString(string(p.Keep)) {
			bad("plans.keep is %q: a whole number of days, hours or minutes, such as 90d, 12h or 30m", p.Keep)
		}

		if p.S3 == nil {
			bad("plans.keep needs plans.s3: it says how long the plans copied there are kept")
		}
	}

	if p.S3 == nil {
		return
	}

	s := *p.S3

	switch {
	case s.Bucket == "":
		bad("plans.s3 needs a bucket")
	case !bucketRE.MatchString(s.Bucket) || strings.Contains(s.Bucket, ".."):
		bad("plans.s3.bucket is %q: a bucket's name is 3 to 63 lowercase letters, digits, dots and hyphens, starting and ending with a letter or digit", s.Bucket)
	}

	switch {
	case s.Region == "":
		bad("plans.s3 needs a region, such as eu-central-1: requests are signed for it")
	case !regionRE.MatchString(s.Region):
		bad("plans.s3.region is %q: lowercase letters, digits and hyphens, such as eu-central-1", s.Region)
	}

	if s.Prefix != "" {
		for part := range strings.SplitSeq(s.Prefix, "/") {
			if !prefixPartRE.MatchString(part) || part == "." || part == ".." {
				bad("plans.s3.prefix is %q: parts of letters, digits, dots, hyphens and underscores, split by single slashes, none at either end", s.Prefix)

				break
			}
		}
	}

	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		if err != nil || !endpointSchemes[u.Scheme] || u.Host == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
			bad("plans.s3.endpoint is %q: the service's base URL, https:// or http:// and a host with an optional port, nothing after it", s.Endpoint)
		}
	}

	for _, kv := range [][2]string{{"access_key_id", s.Credentials.AccessKeyID}, {"secret_access_key", s.Credentials.SecretAccessKey}} {
		key, v := kv[0], kv[1]

		switch {
		case v == "":
			bad("plans.s3.credentials.%s is missing: name the CI var that holds it", key)
		case !varName.MatchString(v):
			bad("plans.s3.credentials.%s must be the name of a var, not %q", key, v)
		}
	}
}

// PlansKeySecrets names each repository task given a var of the key that writes the plans'
// bucket: only the steps that write a run's plan may hold it.
func (c *Config) PlansKeySecrets() []string {
	if c.Plans.S3 == nil {
		return nil
	}

	vars := map[string]bool{c.Plans.S3.Credentials.AccessKeyID: true, c.Plans.S3.Credentials.SecretAccessKey: true}
	delete(vars, "")

	var out []string

	for _, r := range c.RunNames() {
		for _, suite := range slices.Sorted(maps.Keys(c.Runs[r].Suites)) {
			kinds := c.SecretsFor(r, suite).kinds()

			for _, kind := range slices.Sorted(maps.Keys(kinds)) {
				for _, env := range slices.Sorted(maps.Keys(kinds[kind])) {
					if v := kinds[kind][env]; vars[v] {
						out = append(out, fmt.Sprintf("task %s/%s is given %s, a credential of the plans' bucket: "+
							"no task of the repository's may hold it", suite, kind, v))
					}
				}
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}
