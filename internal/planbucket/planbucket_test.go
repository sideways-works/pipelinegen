package planbucket_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/planbucket"
	"github.com/sideways-works/pipelinegen/internal/s3"
	"github.com/sideways-works/pipelinegen/internal/s3/s3test"
)

const head = "ab12345678901234567890123456789012345678"

var at = time.Date(2026, 11, 23, 9, 8, 7, 0, time.UTC)

const plan = `{"version":1,"base":"cd12345678901234567890123456789012345678","head":"` + head + `","all":false,"reason":"","suites":[` +
	`{"suite":"go","all":false,"reason":"","selected":[{"name":"a"},{"name":"b"}],"reasons":{},"untouched":["c","d","e"],"removed":[],"whole_suite":[],` +
	`"estimate":{"selected_seconds":10.4,"all_seconds":50.2,"measured":5,"slices":5}},` +
	`{"suite":"e2e","all":true,"reason":"","selected":[{"name":"x"}],"reasons":{},"untouched":[],"removed":[],"whole_suite":[],` +
	`"estimate":{"selected_seconds":20.3,"all_seconds":20.3,"measured":1,"slices":1}},` +
	`{"suite":"lint","all":false,"reason":"","selected":[],"reasons":{},"untouched":["l"],"removed":[],"whole_suite":[]}]}`

type sleeps []time.Duration

func bucketOf(t *testing.T, srv *s3test.Server, base string, slept *sleeps) (*planbucket.Bucket, *bytes.Buffer) {
	t.Helper()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	warn := &bytes.Buffer{}

	return &planbucket.Bucket{
		Client: &s3.Client{
			Endpoint: u, Region: srv.Region, Bucket: srv.Bucket, PathStyle: true,
			Creds: s3.Credentials{AccessKeyID: srv.KeyID, SecretAccessKey: srv.Secret},
		},
		Base: base, Repository: "owner/repo",
		Now:     func() time.Time { return at },
		Nonce:   func() (string, error) { return "0a1b2c3d", nil },
		Backoff: planbucket.Backoff,
		Sleep: func(_ context.Context, d time.Duration) error {
			*slept = append(*slept, d)

			return nil
		},
		Warn: warn,
	}, warn
}

func TestAPlanGoesUnderItsDateAndItsHead(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)

	var slept sleeps

	b, warn := bucketOf(t, srv, "pipelinegen/owner/repo/", &slept)

	b.Store(context.Background(), "main", "pr-12/feature ü", []byte(plan))

	const (
		planKey = "pipelinegen/owner/repo/2026/11/23/090807Z-main-pr-12_feature__-" + head + "-0a1b2c3d.json"
		headKey = "pipelinegen/owner/repo/heads/ab/" + head + "/20261123T090807Z-main-pr-12_feature__-0a1b2c3d"
	)

	if keys := srv.Keys(); !slices.Equal(keys, []string{planKey, headKey}) {
		t.Fatalf("keys %q", keys)
	}

	obj := srv.Objects()[planKey]

	zr, err := gzip.NewReader(bytes.NewReader(obj.Body))
	if err != nil {
		t.Fatal(err)
	}

	if body, err := io.ReadAll(zr); err != nil || string(body) != plan {
		t.Fatalf("the body is not the plan's bytes gzipped: %v %q", err, body)
	}

	want := map[string]string{
		"Content-Type": "application/vnd.pipelinegen.plan+json", "Content-Encoding": "gzip",
		"X-Amz-Meta-Version": "1", "X-Amz-Meta-Repository": "owner/repo", "X-Amz-Meta-Run": "main", "X-Amz-Meta-Change": "pr-12_feature__",
		"X-Amz-Meta-Head": head, "X-Amz-Meta-Base": "cd12345678901234567890123456789012345678", "X-Amz-Meta-All": "false",
		"X-Amz-Meta-Selected": "3", "X-Amz-Meta-Slices": "7",
		"X-Amz-Meta-Estimated-Seconds-Selected": "31", "X-Amz-Meta-Estimated-Seconds-All": "71",
	}

	for k, v := range want {
		if got := obj.Header.Get(k); got != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}

	pointer := srv.Objects()[headKey]
	if len(pointer.Body) != 0 || pointer.Header.Get("X-Amz-Meta-Plan-Key") != planKey {
		t.Errorf("the pointer: %q %v", pointer.Body, pointer.Header)
	}

	if reqs := srv.Requests(); len(reqs) != 2 || reqs[0].Key != planKey || reqs[1].Key != headKey {
		t.Errorf("the plan goes first, then its pointer: %+v", reqs)
	}

	if warn.Len() != 0 || len(slept) != 0 {
		t.Errorf("warned %q, slept %v", warn, slept)
	}
}

func TestAPlanWithoutEstimatesOrBaseLeavesThoseOut(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)

	var slept sleeps

	b, _ := bucketOf(t, srv, "o/r/", &slept)

	up, err := b.Upload(context.Background(), "nightly", "main",
		[]byte(`{"version":1,"base":"","head":"`+head+`","all":true,"suites":[{"selected":[{"name":"a"}],"untouched":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	h := srv.Objects()[up.PlanKey].Header
	for _, k := range []string{"X-Amz-Meta-Base", "X-Amz-Meta-Estimated-Seconds-Selected", "X-Amz-Meta-Estimated-Seconds-All"} {
		if _, ok := h[k]; ok {
			t.Errorf("%s is set: %q", k, h.Get(k))
		}
	}

	if h.Get("X-Amz-Meta-All") != "true" || h.Get("X-Amz-Meta-Slices") != "1" || !strings.HasPrefix(up.PlanKey, "o/r/2026/11/23/") {
		t.Errorf("%s %v", up.PlanKey, h)
	}
}

func TestAPutThatFailsInPassingIsTriedAgainAfterEachWait(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)

	fails := 3
	srv.Fail = func(r s3test.Request) int {
		if r.Method == http.MethodPut && fails > 0 {
			fails--

			return http.StatusServiceUnavailable
		}

		return 0
	}

	var slept sleeps

	b, warn := bucketOf(t, srv, "o/r/", &slept)
	b.Store(context.Background(), "main", "main", []byte(plan))

	if !slices.Equal(slept, sleeps{time.Second, 4 * time.Second, 16 * time.Second}) || len(srv.Keys()) != 2 || warn.Len() != 0 {
		t.Fatalf("slept %v, keys %q, warned %q", slept, srv.Keys(), warn)
	}
}

func TestAPlanTheBucketRefusesIsAWarningAndNothingIsCleaned(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)
	srv.Fail = func(s3test.Request) int { return http.StatusInternalServerError }

	var slept sleeps

	b, warn := bucketOf(t, srv, "o/r/", &slept)
	b.Keep = time.Hour
	b.Store(context.Background(), "main", "main", []byte(plan))

	reqs := srv.Requests()
	if len(reqs) != 4 || len(slept) != 3 || !strings.Contains(warn.String(), "the plan is not in the bucket") {
		t.Fatalf("%d requests, slept %v, warned %q", len(reqs), slept, warn)
	}

	for _, r := range reqs {
		if r.Method != http.MethodPut {
			t.Errorf("after a failed upload: %s %s", r.Method, r.Key)
		}
	}

	// a refusal is tried as often: S3 refuses a slow upload with a 400
	srv.Fail = func(s3test.Request) int { return http.StatusBadRequest }
	slept = nil

	b.Store(context.Background(), "main", "main", []byte(plan))

	if n := len(srv.Requests()) - len(reqs); n != 4 || len(slept) != 3 {
		t.Errorf("a 400 was sent %d times, slept %v", n, slept)
	}
}

func TestCleanupDeletesTheDaysThatEndedBeforeKeep(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)
	srv.PageSize = 2

	const base = "p/o/r/"

	keep := []string{
		base + "2026/08/25/235959Z-main-main-x-00000000.json", // partly inside keep
		base + "2026/11/23/000000Z-main-main-x-00000000.json",
		base + "heads/ab/" + head + "/20260825T100000Z-main-main-00000000",
		base + "heads/cd/x/20200101T000000Z-main-main-00000000", // another shard
		base + "2026/xx/01/a.json",                              // no month's folder
		base + "notes/2020.json",
		"p/o/other/2020/01/01/a.json", // another repository
	}
	gone := []string{
		base + "2025/12/31/000000Z-main-main-x-00000000.json",
		base + "2026/01/15/000000Z-main-main-x-00000000.json",
		base + "2026/01/15/000001Z-main-main-x-00000000.json",
		base + "2026/08/24/235959Z-main-main-x-00000000.json",
		base + "heads/ab/" + head + "/20260825T085959Z-main-main-00000000",
		base + "heads/ab/ab99999999999999999999999999999999999999/20250101T000000Z-main-main-00000000",
	}

	for _, k := range append(slices.Clone(keep), gone...) {
		srv.Put(k)
	}

	var slept sleeps

	b, _ := bucketOf(t, srv, base, &slept)
	b.Keep = 90 * 24 * time.Hour

	// 90 days before 2026-11-23 10:00 is 2026-08-25 10:00
	if err := b.Cleanup(context.Background(), head, time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	slices.Sort(keep)

	if got := srv.Keys(); !slices.Equal(got, keep) {
		t.Errorf("kept %q\nwant %q", got, keep)
	}

	for _, r := range srv.Requests() {
		if r.Method == http.MethodGet && (r.Query["prefix"] == base+"2026/11/" || r.Query["prefix"] == base+"2026/09/") {
			t.Errorf("listed %s, whose every day is inside keep", r.Query["prefix"])
		}
	}

	// without keep nothing is listed or deleted
	before := len(srv.Requests())
	b.Keep = 0

	if err := b.Cleanup(context.Background(), head, at); err != nil || len(srv.Requests()) != before {
		t.Errorf("%v, %d requests", err, len(srv.Requests())-before)
	}
}

func TestACleanupThatFailsOnlyWarns(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)
	srv.Put("o/r/2020/01/01/a.json")
	srv.Fail = func(r s3test.Request) int {
		if r.Method == http.MethodDelete {
			return http.StatusInternalServerError
		}

		return 0
	}

	var slept sleeps

	b, warn := bucketOf(t, srv, "o/r/", &slept)
	b.Keep = 24 * time.Hour
	b.Store(context.Background(), "main", "main", []byte(plan))

	if !strings.Contains(warn.String(), "cleanup: deletes failed: 1 of 1") || len(srv.Keys()) != 3 {
		t.Errorf("warned %q, keys %q", warn, srv.Keys())
	}
}

func TestTheRepositoryIsTheForgesOrThePipelines(t *testing.T) {
	for _, tc := range []struct {
		cfg         config.Config
		owner, repo string
	}{
		{config.Config{Pipeline: "p", GitHub: config.GitHub{Repository: "acme/shop"}}, "acme", "shop"},
		{config.Config{Pipeline: "p", Git: config.Git{Remote: "https://git.example/group/sub/shop.git"}}, "group_sub", "shop"},
		{config.Config{Pipeline: "p", Git: config.Git{Remote: "git@git.example:acme/shop.git"}}, "acme", "shop"},
		{config.Config{Pipeline: "p", Forges: []config.Forge{{Name: "git", Remote: "ssh://git@git.example/acme/tools"}}}, "acme", "tools"},
		{config.Config{Pipeline: "my pipeline"}, "_", "my_pipeline"},
	} {
		if o, r := planbucket.RepositoryOf(&tc.cfg); o != tc.owner || r != tc.repo {
			t.Errorf("%+v: %s/%s, want %s/%s", tc.cfg, o, r, tc.owner, tc.repo)
		}
	}
}

func TestTheBaseKeyStartsAtTheOwnerWithoutAPrefix(t *testing.T) {
	for prefix, want := range map[string]string{"": "acme/shop/", "ci/pipelinegen": "ci/pipelinegen/acme/shop/"} {
		cfg := &config.Config{GitHub: config.GitHub{Repository: "acme/shop"}, Plans: config.Plans{S3: &config.PlansS3{Bucket: "b", Region: "eu-central-1", Prefix: prefix}}}

		b, err := planbucket.New(cfg, s3.Credentials{})
		if err != nil || b.Base != want || b.Client.Endpoint.String() != "https://s3.eu-central-1.amazonaws.com" || b.Client.PathStyle {
			t.Errorf("%q: %v %+v", prefix, err, b)
		}
	}
}
