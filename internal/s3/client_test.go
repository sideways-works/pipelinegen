package s3_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/s3"
	"github.com/sideways-works/pipelinegen/internal/s3/s3test"
)

func clientOf(t *testing.T, srv *s3test.Server) *s3.Client {
	t.Helper()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return &s3.Client{
		Endpoint: u, Region: srv.Region, Bucket: srv.Bucket, PathStyle: srv.PathStyle,
		Creds: s3.Credentials{AccessKeyID: srv.KeyID, SecretAccessKey: srv.Secret},
	}
}

func TestPutListAndDeleteSignedInPathStyle(t *testing.T) {
	srv := s3test.New(t, "plans", true)
	c := clientOf(t, srv)
	ctx := context.Background()

	keys := []string{"p/a/2026/01/01/x.json", "p/a/2026/01/02/y.json", "p/a/2026/02/01/z.json", "p/a/heads/ab/c", "p/a/odd key+~.json"}
	for _, k := range keys {
		if err := c.Put(ctx, k, []byte("body "+k), map[string]string{"Content-Type": "application/json"}, map[string]string{"run": "main"}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	got := srv.Objects()["p/a/odd key+~.json"]
	if string(got.Body) != "body p/a/odd key+~.json" || got.Header.Get("X-Amz-Meta-Run") != "main" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("stored %q %v", got.Body, got.Header)
	}

	// two entries a page: every page's token holds characters a query must encode
	srv.PageSize = 2

	all, err := c.List(ctx, "p/a/", "")
	if err != nil || !slices.Equal(all.Keys, srv.Keys()) || len(all.Prefixes) != 0 {
		t.Fatalf("list: %v %+v", err, all)
	}

	months, err := c.List(ctx, "p/a/2026/", "/")
	if err != nil || len(months.Keys) != 0 || !slices.Equal(months.Prefixes, []string{"p/a/2026/01/", "p/a/2026/02/"}) {
		t.Fatalf("list by level: %v %+v", err, months)
	}

	top, err := c.List(ctx, "p/a/", "/")
	if err != nil || !slices.Equal(top.Keys, []string{"p/a/odd key+~.json"}) || !slices.Equal(top.Prefixes, []string{"p/a/2026/", "p/a/heads/"}) {
		t.Fatalf("top level: %v %+v", err, top)
	}

	for range 2 {
		if err := c.Delete(ctx, "p/a/2026/01/01/x.json"); err != nil {
			t.Fatalf("a delete, again: %v", err)
		}
	}

	if slices.Contains(srv.Keys(), "p/a/2026/01/01/x.json") {
		t.Fatal("not deleted")
	}

	pages := 0

	for _, r := range srv.Requests() {
		if r.Status != http.StatusOK && r.Status != http.StatusNoContent {
			t.Errorf("%s %s answered %d", r.Method, r.Key, r.Status)
		}

		if r.Query["continuation-token"] != "" {
			pages++
		}
	}

	if pages == 0 {
		t.Error("no listing went past its first page")
	}
}

func TestVirtualHostedStyleSignsTheBucketsHost(t *testing.T) {
	srv := s3test.New(t, "plans", false)
	addr := strings.TrimPrefix(srv.URL, "http://")

	c := clientOf(t, srv)
	c.Endpoint = &url.URL{Scheme: "http", Host: "s3.test:9000"}
	c.HTTP = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}

	if err := c.Put(context.Background(), "a/b.json", []byte("{}"), nil, nil); err != nil {
		t.Fatal(err)
	}

	if l, err := c.List(context.Background(), "a/", ""); err != nil || !slices.Equal(l.Keys, []string{"a/b.json"}) {
		t.Fatalf("%v %+v", err, l)
	}
}

func TestARefusalNamesItsCodeAndNothingItEchoes(t *testing.T) {
	srv := s3test.New(t, "plans", true)
	c := clientOf(t, srv)
	c.Creds.SecretAccessKey = "wrong"

	err := c.Put(context.Background(), "k", []byte("x"), nil, nil)

	var e *s3.Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Code != "SignatureDoesNotMatch" || s3.Transient(err) {
		t.Fatalf("%v", err)
	}

	for _, leak := range []string{srv.KeyID, "secret material", "AWS4-HMAC-SHA256", "Signature="} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %v", leak, err)
		}
	}

	srv.Fail = func(s3test.Request) int { return http.StatusServiceUnavailable }
	c.Creds.SecretAccessKey = srv.Secret

	if err := c.Put(context.Background(), "k", []byte("x"), nil, nil); !s3.Transient(err) {
		t.Fatalf("a 503 is transient: %v", err)
	}
}
