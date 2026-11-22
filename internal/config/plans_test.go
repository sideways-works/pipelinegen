package config

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const plansSection = `plans:
  s3:
    bucket: ci-plans
    prefix: pipelinegen
    region: eu-central-1
    endpoint: https://s3.example.com
    path_style: true
    credentials: {access_key_id: plans_key_id, secret_access_key: plans_secret}
  keep: 90d
`

func TestThePlansSectionReads(t *testing.T) {
	c := parseTrusted(t, trustedBase+plansSection)

	s := c.Plans.S3
	if s == nil || s.Bucket != "ci-plans" || s.Prefix != "pipelinegen" || s.Region != "eu-central-1" || !s.PathStyle ||
		s.EndpointURL() != "https://s3.example.com" || s.Credentials != (PlansCredentials{"plans_key_id", "plans_secret"}) {
		t.Fatalf("%+v", s)
	}

	if d, ok := c.Plans.KeepFor(); !ok || d != 90*24*time.Hour {
		t.Errorf("keep %v %v", d, ok)
	}

	aws := PlansS3{Region: "eu-west-1"}
	if aws.EndpointURL() != "https://s3.eu-west-1.amazonaws.com" {
		t.Errorf("AWS's endpoint: %s", aws.EndpointURL())
	}

	without := parseTrusted(t, trustedBase)
	if _, ok := without.Plans.KeepFor(); ok || without.Plans.S3 != nil {
		t.Errorf("no section: %+v", without.Plans)
	}
}

func TestABadPlansSectionIsRefusedSayingWhy(t *testing.T) {
	for edit, want := range map[[2]string]string{
		{"    bucket: ci-plans\n", ""}:                                               "plans.s3 needs a bucket",
		{"bucket: ci-plans", "bucket: CI_plans"}:                                     "plans.s3.bucket is \"CI_plans\"",
		{"    region: eu-central-1\n", ""}:                                           "plans.s3 needs a region",
		{"prefix: pipelinegen", "prefix: /pipelinegen/"}:                             "plans.s3.prefix is",
		{"prefix: pipelinegen", "prefix: a//b"}:                                      "plans.s3.prefix is",
		{"endpoint: https://s3.example.com", "endpoint: ftp://x"}:                    "plans.s3.endpoint is",
		{"endpoint: https://s3.example.com", "endpoint: https://s3.example.com/b"}:   "plans.s3.endpoint is",
		{"endpoint: https://s3.example.com", "endpoint: https://k:s@s3.example.com"}: "plans.s3.endpoint is",
		{"access_key_id: plans_key_id, ", ""}:                                        "plans.s3.credentials.access_key_id is missing",
		{"plans_secret", "((plans_secret))"}:                                         "plans.s3.credentials.secret_access_key must be the name of a var",
		{"keep: 90d", "keep: 2w"}:                                                    `plans.keep is "2w"`,
		{"keep: 90d", "keep: 0d"}:                                                    `plans.keep is "0d"`,
		{"keep: 90d", "keep: 90"}:                                                    `plans.keep is "90"`,
	} {
		text := trustedBase + strings.Replace(plansSection, edit[0], edit[1], 1)

		_, err := Parse([]byte(text))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q -> %q: %v", edit[0], edit[1], err)
		}
	}

	if _, err := Parse([]byte(trustedBase + "plans: {keep: 30d}\n")); err == nil || !strings.Contains(err.Error(), "plans.keep needs plans.s3") {
		t.Errorf("keep alone: %v", err)
	}

	for _, keep := range []string{"12h", "30m", "1d"} {
		parseTrusted(t, trustedBase+strings.Replace(plansSection, "keep: 90d", "keep: "+keep, 1))
	}
}

func TestAChangeCantMoveWhereThePlansGo(t *testing.T) {
	trusted := parseTrusted(t, trustedBase+plansSection).Trusted()

	changed := parseTrusted(t, trustedBase+strings.NewReplacer("bucket: ci-plans", "bucket: elsewhere", "keep: 90d", "keep: 1m").Replace(plansSection))
	if edited := changed.WithTrusted(trusted); !slices.Contains(edited, "plans") ||
		changed.Plans.S3.Bucket != "ci-plans" || changed.Plans.Keep != "90d" {
		t.Errorf("edited %v, plans %+v", edited, changed.Plans)
	}

	// a change that adds the section gets none
	added := parseTrusted(t, trustedBase+plansSection)
	if edited := added.WithTrusted(parseTrusted(t, trustedBase).Trusted()); !slices.Contains(edited, "plans") || added.Plans.S3 != nil {
		t.Errorf("edited %v, plans %+v", edited, added.Plans)
	}

	// an older pipeline's trusted keys, without plans, are no edit of a config without them
	var older Trusted
	if err := json.Unmarshal([]byte(`{"github_repository":"o/r"}`), &older); err != nil {
		t.Fatal(err)
	}

	plain := parseTrusted(t, trustedBase)
	if edited := plain.WithTrusted(older); slices.Contains(edited, "plans") {
		t.Errorf("edited %v", edited)
	}

	// the trusted copy is the config's own, not shared with it
	c := parseTrusted(t, trustedBase+plansSection)
	tr := c.Trusted()
	tr.Plans.S3.Bucket = "other"

	if c.Plans.S3.Bucket != "ci-plans" {
		t.Error("the trusted keys share the config's plans")
	}
}

func TestNoTaskOfTheRepositoryHoldsThePlansKey(t *testing.T) {
	text := trustedBase + plansSection + `runs:
  main:
    trigger: {branch: main}
    suites:
      e2e: {secrets: {batch: {AWS_SECRET: plans_secret}}}
`

	_, err := Parse([]byte(text))
	if err == nil || !strings.Contains(err.Error(), "task e2e/batch is given plans_secret, a credential of the plans' bucket") {
		t.Fatalf("%v", err)
	}
}

func TestWhereThePlansGoIsObservableByNoTest(t *testing.T) {
	m := meaningOf(t, nil, []string{"pipeline: shop\n", "pipeline: shop\n" + plansSection})

	if !m.Changed() || !m.Understood() || len(runs(m, "go", "a_test.go")) != 0 {
		t.Fatalf("parts %+v", m.Parts)
	}
}
