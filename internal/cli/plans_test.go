package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/s3/s3test"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAStepWithoutThePlansKeyWarnsOnceAndUploadsNothing(t *testing.T) {
	cfg := &config.Config{Pipeline: "shop", Plans: config.Plans{S3: &config.PlansS3{Bucket: "ci-plans", Region: "eu-central-1"}}}

	var warn bytes.Buffer

	env := map[string]string{plugin.EnvPlansAccessKeyID: "AKID"}

	if b := plansBucket(cfg, func(k string) string { return env[k] }, &warn); b != nil {
		t.Fatalf("a bucket without its secret key: %+v", b)
	}

	if strings.Count(warn.String(), "\n") != 1 || !strings.Contains(warn.String(), plugin.EnvPlansSecretAccessKey) || strings.Contains(warn.String(), "AKID") {
		t.Errorf("warned %q", warn.String())
	}

	warn.Reset()

	if b := plansBucket(&config.Config{}, func(string) string { return "" }, &warn); b != nil || warn.Len() != 0 {
		t.Errorf("no plans section: %+v %q", b, warn.String())
	}
}

func TestTheStepsKeyUploadsThePlanItWrote(t *testing.T) {
	srv := s3test.New(t, "ci-plans", true)

	cfg := &config.Config{
		Pipeline: "shop", GitHub: config.GitHub{Repository: "acme/shop"},
		Plans: config.Plans{S3: &config.PlansS3{Bucket: srv.Bucket, Region: srv.Region, Endpoint: srv.URL, PathStyle: true, Prefix: "pg"}},
	}

	env := map[string]string{plugin.EnvPlansAccessKeyID: srv.KeyID, plugin.EnvPlansSecretAccessKey: srv.Secret}

	var warn bytes.Buffer

	b := plansBucket(cfg, func(k string) string { return env[k] }, &warn)
	if b == nil {
		t.Fatalf("no bucket: %q", warn.String())
	}

	head := strings.Repeat("ab", 20)
	b.Store(context.Background(), "pr", "pr-12", []byte(`{"version":1,"head":"`+head+`","suites":[]}`))

	keys := srv.Keys()
	if len(keys) != 2 || !strings.HasPrefix(keys[0], "pg/acme/shop/") || !strings.Contains(keys[0], "-pr-pr-12-"+head+"-") || warn.Len() != 0 {
		t.Errorf("keys %q, warned %q", keys, warn.String())
	}
}

// The second step of a proposed change's planning writes the plan and copies the very bytes it
// wrote; the first step, which runs the change's code, refuses to start holding the key.
func TestTheSecondStepCopiesThePlanItWroteAndTheFirstHoldsNoKey(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)
	cfg := loadConfig(t, root)

	srv := s3test.New(t, "ci-plans", true)
	cfg.Plans.S3 = &config.PlansS3{Bucket: srv.Bucket, Region: srv.Region, Endpoint: srv.URL, PathStyle: true}

	t.Setenv(plugin.EnvPlansAccessKeyID, srv.KeyID)
	t.Setenv(plugin.EnvPlansSecretAccessKey, srv.Secret)
	t.Setenv(plugin.EnvPlansSessionToken, "")

	if err := planOut(t.Context(), cfg, ch, s, trigger, t.TempDir(), "", t.TempDir(), os.Stdout); !errors.Is(err, errWritingCredential) ||
		!strings.Contains(err.Error(), plugin.EnvPlansSecretAccessKey) {
		t.Fatalf("the first step holding the plans key: %v", err)
	}

	// the first step left its store read-only; the second step's is another
	s.ReadOnly = false

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), cfg, ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	own := t.TempDir()
	writeFile(t, filepath.Join(own, planOutFile), fmt.Sprintf(`{"base": %q, "head": %q, "all": false, "reason": "", "suites": [{"suite": "e2e", "all": false, "reason": "", "selected": [{"label": "//e2e:a", "sources": []}], "untouched": [], "whole_suite": []}]}`, base, head))

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, own, os.Stdout); err != nil || !handOn {
		t.Fatal(handOn, err)
	}

	ref, ok, err := s.First(t.Context(), []string{reportID(ch).PlanRef()})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}

	written, _, err := s.File(t.Context(), ref.Commit, "plan.json")
	if err != nil {
		t.Fatal(err)
	}

	keys := srv.Keys()
	if len(keys) != 2 || !strings.Contains(keys[0], "/pr/pr-7/") || !strings.Contains(keys[0], "Z-"+head+"-") || !strings.Contains(keys[1], "/heads/"+head[:2]+"/"+head+"/") {
		t.Fatalf("keys %q", keys)
	}

	zr, err := gzip.NewReader(bytes.NewReader(srv.Objects()[keys[0]].Body))
	if err != nil {
		t.Fatal(err)
	}

	if body, err := io.ReadAll(zr); err != nil || !bytes.Equal(body, written) || !bytes.Contains(body, []byte(`"version":1`)) {
		t.Errorf("uploaded %q\nwritten %q", body, written)
	}
}
