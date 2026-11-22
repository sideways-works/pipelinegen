package cli

import (
	"context"
	"io"
	"os"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/planbucket"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/s3"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// keepPlan copies the plan a trusted step just wrote to the run's records to the plans' bucket,
// when the config names one. Only startRun calls it: `plan --out`, which runs a proposed change's
// code, writes no record and uploads nothing. Whatever fails is a warning; the plan stands.
func keepPlan(ctx context.Context, cfg *config.Config, id report.ID, data []byte) {
	if b := plansBucket(cfg, os.Getenv, os.Stderr); b != nil {
		b.Store(ctx, id.Run, id.Change, data)
	}
}

// plansBucket is the bucket plans.s3 names with the key in the step's environment; nil without
// either, warning once when the config names a bucket the step has no key for.
func plansBucket(cfg *config.Config, getenv func(string) string, warn io.Writer) *planbucket.Bucket {
	if cfg.Plans.S3 == nil {
		return nil
	}

	creds := s3.Credentials{
		AccessKeyID: getenv(plugin.EnvPlansAccessKeyID), SecretAccessKey: getenv(plugin.EnvPlansSecretAccessKey),
		SessionToken: getenv(plugin.EnvPlansSessionToken),
	}

	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		warnf(warn, "pipelinegen plan: plans.s3 names a bucket, but %s and %s are not both in this step's environment: the plan is not copied there\n",
			plugin.EnvPlansAccessKeyID, plugin.EnvPlansSecretAccessKey)

		return nil
	}

	// masked wherever an error might carry them
	secret.Add(creds.SecretAccessKey)
	secret.Add(creds.SessionToken)

	b, err := planbucket.New(cfg, creds)
	if err != nil {
		warnf(warn, "pipelinegen plan: plans.s3: %v\n", err)

		return nil
	}

	b.Warn = warn

	return b
}
