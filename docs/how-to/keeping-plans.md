# Keeping plans in a bucket

Every run starts with a plan: which slices of which suites run, and why.
pipelinegen keeps it with the run's records, which the sweep removes after
`keep.runs` (7 days by default). To keep plans longer, or to read them
from somewhere other than the repository, pipelinegen can also copy each
plan it writes into a bucket of an S3-compatible service: AWS S3,
SeaweedFS, MinIO and others that speak the same API. A tool that lists and
reads the bucket then sees every plan of every run, by repository, by date
and by commit, without access to the repository at all.

## The config

```yaml
plans:
  s3:
    bucket: ci-plans                    # required in the section
    prefix: pipelinegen                 # optional, default none
    region: eu-central-1                # required
    endpoint: https://s3.example.com    # optional: AWS's https://s3.<region>.amazonaws.com when left out
    path_style: true                    # optional, default false (virtual-hosted)
    credentials: {access_key_id: plans_key_id, secret_access_key: plans_secret}
  keep: 90d                             # optional; without it nothing is deleted
```

- `bucket` and `region` are required. Requests are signed for the region
  (AWS Signature Version 4); a service with no regions of its own takes
  any, such as `us-east-1`.
- `endpoint` is the service's base URL, `https://` or `http://` with a
  host and an optional port, nothing after it. Left out, it is AWS's for
  the region.
- `path_style: true` puts the bucket in the path
  (`https://s3.example.com/ci-plans/<key>`) instead of the host
  (`https://ci-plans.s3.example.com/<key>`). SeaweedFS and MinIO usually
  want it; so does a bucket whose name holds a dot over https.
- `prefix` is where in the bucket the plans go: parts of letters, digits,
  `.`, `_` and `-`, split by single slashes, none at either end.
- `credentials` names the two CI vars that hold the access key's id and
  secret, never their values, as `github.token` names its var.
- `keep` is how long a plan is kept: a whole number of days, hours or
  minutes (`90d`, `12h`, `30m`). Without it pipelinegen deletes nothing;
  a [lifecycle rule](#a-lifecycle-rule-instead) can do it instead.

`plans` is one of the [keys a proposed change's config can't
change](secrets.md#what-a-proposed-changes-config-cant-change): a pull
request that names another bucket or endpoint is planned with the
repository's, and its plan says it edited `plans`. A change to the
section runs no test: no test can observe where plans are copied.

The key's least rights are `s3:PutObject`, plus `s3:ListBucket` and
`s3:DeleteObject` when pipelinegen deletes old plans, all on the bucket
and the prefix alone.

## Which step uploads, and why only that one

The step that writes a run's plan to the run's records uploads it, right
after writing it, and only once it did: a step that finds the plan written
by another uploads nothing. Those steps are:

- the planning step of a run of a branch, a timer or by hand (and of a
  continuous run);
- the second step of a pull request's planning (`plan --from`, also with
  `--emit` on piperun), and the sweep's step that writes the plans it
  made.

The first step of a pull request's planning (`plan --out`) never uploads.
It runs the pull request's own code: its Bazel queries and the plugins
reading its tree. A key in that step could be read by the change, which
could then write anything into the bucket. That step holds the read token
alone, and refuses to start when it finds `PIPELINEGEN_PLANS_ACCESS_KEY_ID`,
`PIPELINEGEN_PLANS_SECRET_ACCESS_KEY` or `PIPELINEGEN_PLANS_SESSION_TOKEN`
in its environment. No task of the repository's may be given either var
in its `secrets`: the config is refused for it.

The key reaches the step through its environment only:

| Variable | Holds |
|---|---|
| `PIPELINEGEN_PLANS_ACCESS_KEY_ID` | the access key's id, from the var `credentials.access_key_id` names |
| `PIPELINEGEN_PLANS_SECRET_ACCESS_KEY` | its secret, from the var `credentials.secret_access_key` names |
| `PIPELINEGEN_PLANS_SESSION_TOKEN` | optional: the session token of a temporary key |

The runner gives the two vars to the steps above and to no other; the
runner's spec carries their names as `plans`. When the config names a
bucket and a step that writes a plan lacks the id or the secret, the step
says so once on stderr and uploads nothing. The key, the request's
signature and its `Authorization` header are never printed: an error says
the request, the HTTP status and the service's error code and message,
nothing it echoes back. Bazel, bazel-diff and the plugins run without
any `PIPELINEGEN_` variable, these among them.

## The layout: a contract

A tool reads the bucket by these keys and this metadata alone, so they
change only with a release that says so. All times are UTC, the time of
the upload. `<base>` is `<prefix>/<owner>/<repo>/`, or `<owner>/<repo>/`
without a prefix.

The plan:

```text
<base><yyyy>/<mm>/<dd>/<hhmmss>Z-<run>-<change>-<head>-<nonce>.json
pipelinegen/acme/shop/2026/11/23/090807Z-pr-pr-12-ab3f9c0d2e7b41a8c6d5e0f1a2b3c4d5e6f70812-0a1b2c3d.json
```

The head's pointer to it, put after the plan:

```text
<base>heads/<head[0:2]>/<head>/<yyyymmdd>T<hhmmss>Z-<run>-<change>-<nonce>
pipelinegen/acme/shop/heads/ab/ab3f9c0d2e7b41a8c6d5e0f1a2b3c4d5e6f70812/20261123T090807Z-pr-pr-12-0a1b2c3d
```

- `<owner>/<repo>` is the repository the config names: `github.repository`;
  else the last two parts of the code forge's `remote` or of `git.remote`
  (`git@host:acme/shop.git` is `acme/shop`; a remote whose path has more
  parts joins those before the last, `group/sub/shop` is `group_sub/shop`);
  else `_/<pipeline>`.
- `<run>` is the run's name and `<change>` the change's, as the run's
  records name it: `main` for a branch, `pr-12` for pull request 12.
- In owner, repo, run and change, each character outside `[A-Za-z0-9._-]`
  becomes `_`.
- `<head>` is the plan's `head`: the 40 hex digits of the commit planned.
  For a run that compares with `merged_target` that is the merge of the
  change into its target, and the change's own commit is the plan's
  `merged`.
- `<nonce>` is 8 random lowercase hex digits, so two plans of the same
  head in the same second don't meet. The plan and its pointer share it.
- The year, month and day folders sort by time, and `heads/` never looks
  like a year. To find the plans of a commit, list
  `<base>heads/<head[0:2]>/<head>/`: each pointer is empty and names its
  plan in its metadata.

The plan's object:

- Its body is the plan's JSON, exactly the bytes written to the run's
  records ([the plan's JSON](../contracts.md#the-plans-json), with its
  `version`), gzipped.
- `Content-Type: application/vnd.pipelinegen.plan+json` and
  `Content-Encoding: gzip`: a client that honours the encoding reads the
  JSON.
- Its user metadata (`x-amz-meta-<name>`), every value printable ASCII
  (any other character becomes `_`):

| Name | Value |
|---|---|
| `version` | the plan's `version` |
| `repository` | `<owner>/<repo>`, as in the key |
| `run` | the run, as in the key |
| `change` | the change, as in the key |
| `head` | the plan's `head` |
| `base` | the plan's `base`; left out when the plan has none |
| `all` | `true` or `false`, the plan's `all` |
| `selected` | the slices the plan selected, over all suites |
| `slices` | every slice of the suites, selected or not, over all suites |
| `estimated-seconds-selected` | the sum of the suites' `estimate.selected_seconds`, in whole seconds; left out when no suite has an estimate |
| `estimated-seconds-all` | the sum of the suites' `estimate.all_seconds`, in whole seconds; left out with the other |

The pointer has one: `plan-key`, the plan's key.

## Deleting old plans

With `keep`, the step deletes what has aged out after each plan it
uploaded. It takes no lock and waits for nothing: two steps deleting the
same plan both succeed.

- It lists `<base>` level by level (ListObjectsV2 with the delimiter `/`):
  the years, the months of each year, the days of each month. A year or
  month whose first day ended within `keep` is not listed further.
- It deletes every object of each day folder whose whole day ended before
  now less `keep`. A day partly inside `keep` is kept, so a plan may stay
  up to a day longer than `keep`.
- It prunes one shard of the head index, `<base>heads/<head[0:2]>/` of the
  plan it just uploaded: each pointer whose key's time is older than now
  less `keep`. Every head's shard is pruned when a plan of a head in it is
  uploaded, so a pointer of a shard no new head falls into stays until one
  does.

### A lifecycle rule instead

A bucket can expire the plans itself, with no list or delete by
pipelinegen: leave `keep` out and give the bucket a lifecycle rule. On AWS
(`aws s3api put-bucket-lifecycle-configuration --bucket ci-plans
--lifecycle-configuration file://lifecycle.json`), expiring everything
under the prefix 90 days after it was written, pointers included:

```json
{
  "Rules": [
    {
      "ID": "expire-pipelinegen-plans",
      "Filter": {"Prefix": "pipelinegen/"},
      "Status": "Enabled",
      "Expiration": {"Days": 90}
    }
  ]
}
```

Other services set such a rule their own way: see their documentation for
expiring objects by prefix. With the rule, the key needs `s3:PutObject`
alone.

## What a failure does

Nothing a bucket does fails a plan. The plan is in the run's records
before the upload starts, and the run goes on as it would without the
bucket:

- Each PUT is tried once and, while its failure may pass (no answer, a
  5xx, a 408 or a 429), again after 1, 4 and 16 seconds: four tries at
  most. A refusal that trying again would only repeat (a 403 for a wrong
  key, a bucket that doesn't exist) is not tried again. Then the step
  says `pipelinegen plan: plans.s3: the plan is not in the bucket: ...`
  on stderr and goes on. The plan is not uploaded later.
- A pointer that can't be put is a warning too; the plan stays listed by
  its date.
- A cleanup runs only after the plan was uploaded. A listing or a delete
  that fails is a warning naming how many deletes failed and the first
  failure; the next upload's cleanup finds what was left.
