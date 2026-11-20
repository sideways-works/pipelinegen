# Writing a forge

> **Not in a release yet.** The forge protocol is written down in
> [the plugin protocol](../plugins.md#forges). The core calls two
> forge plugins: the GitHub forge for a config's `github:` section,
> and the git forge for a plain git server under
> [`forges`](../reference/config.md#forges). Both are pinned by
> pipelinegen and fetched when a config uses them. This page describes the
> design as a whole, of which config for other forges is still to come;
> the protocol page has every call and field.

A forge tells pipelinegen where the code lives: the git host of the
repository under test. It finds the repository from its remote URL, lists
the changes proposed for a branch (pull requests, merge requests), and
shows results there: a status per commit and one comment per proposed
change. pipelinegen's core then knows no git host; GitHub, GitLab and
Gitea are forge plugins, and the git forge covers a plain git
server with no host at all.

## Requests as data

A forge holds no secret and sends nothing. Like every plugin it is a
plain program without network access. For each call it answers with the
HTTP requests to make, as data, and names the environment variable whose
token each request needs; the core sends the requests, holds the tokens,
retries and keeps to rate limits, and hands the responses back for the
forge to read.

The core checks every request before it sends one, because a forge is
third-party code that must never be able to send a token anywhere else:

- a request goes only to the scheme and host of the remote the forge
  recognised, or to an API host the forge declares in `describe` and
  the config confirms;
- it carries only the header names `describe` declares;
- requests and pages per call are bounded;
- a redirect to another host is never followed with a token.

## The calls

| Call | What the forge answers |
|---|---|
| `describe` | its kind, its capabilities (statuses, a comment, proposed changes, drafts, labels, when a change became ready, forks, the head ref), its limits (how long a status text or a comment may be, whether the host renders Markdown and Mermaid), the API hosts it may call, the header names it uses, and an environment variable per credential purpose |
| `identify {remote}` | whether the remote is its host's, the repository's id, its web address, the git remote to fetch from, and the ref that holds a proposed change's head (`refs/pull/{id}/head`, `refs/merge-requests/{id}/head`, or none, in which case the core fetches the head branch and checks the commit) |
| `changes {base, filters}` | the requests that list proposed changes into a base branch, then, from their responses, each change: its id, base, head commit and branch, whether it is a draft, its labels, whether it comes from the same repository or a fork (or that it can't tell, which counts as a fork), when it was updated and became ready |
| `status {commit, context, state, text, url}` | the request that sets one commit status; the core's four states (pending, success, failure, error) are mapped to the host's own |
| `statuses {commit}` | the requests that read a commit's statuses, then the newest status per context |
| `subject {commit}` | the request that reads a commit's subject line |
| `comment {change, marker, body}` | the requests that create the change's one comment or update it, finding it by its hidden marker and dropping duplicates |
| `links {commit, file, change}` | web links to a commit, a file at a commit, a change |

A forge also gives a fallback: a template per status state, which the
runner renders into the pipeline when it is generated, so a job can still
report when pipelinegen itself couldn't be fetched.

Downloads are not a forge's. Pins resolve to plain URLs with a
checksum when the pipeline is generated; `repository` plus `release`
stays a shorthand for GitHub releases.

## Credentials by purpose

A forge names the token it needs for each purpose, never a value:

| Purpose | For |
|---|---|
| `read` | fetching the code |
| `changes` | listing proposed changes |
| `report` | statuses and comments |

The config maps each purpose to a CI variable (one variable can serve
several). The core attaches a credential only to a request of its purpose.
A task that runs a change's code (a prepare task, a batch, a whole-suite
check, any Bazel run on the change's tree) gets at most the `read`
credential, and a change from a fork never runs with a token. Writing the
store is the store plugin's purpose, not a forge's ([writing a
store](store.md)).

## Proposed changes and forks

The forge's `changes` is how pipelinegen discovers proposed changes on
every host: the pipeline's sweep calls it, plans each new head, and hands
it to the suites. A host's own trigger (a pull request resource) can make
that faster, but is never the only path. Change ids carry the forge,
`<forge>/<kind>-<n>`, so two hosts' numbers never collide.

The conformance kit's forge section checks the fork rule: a change
runs with credentials only when the forge knows it comes from the
same repository, and a filter the forge can't answer (drafts, labels,
readiness on a host without them) fails the config's validation rather
than letting everything through.

## Plain git

The git forge covers a git server with no host API. Proposed changes
are branches that match a pattern of the config. The core lists the
branches with `git ls-remote --heads` and the read credential, and hands
them to the forge as `heads`; the forge picks the changes and
sends no request. There are no drafts or labels. It reports nowhere on a
host: results live in the store's records, and `pipelinegen status
<commit or branch>` prints a run's state for a script or a server's hook.
ssh remotes take their key from a CI variable and require known host keys.
A plain git server has no forks, so the forge says every change comes
from the repository itself, and its kit fixture says why in `no_forks`.

## Testing one

`pipelinegen plugin test <the forge> --tree <fixture>` runs the
[conformance kit](../plugins.md#conformance) on it: every call it
declares, against a fake host that serves the fixture's responses, with
the core's own checks on every request. The GitHub and git forges run
it in their own tests.

## The config, as designed

Today the `github` section uses the GitHub forge, and `forges`
takes the git forge, with the keys in [the config
reference](../reference/config.md#forges); a `plugins` entry of kind
`forge` pins either. Naming any forge plugin under `forges`
is designed to look like this:

```yaml
forges:
  - name: github
    plugin: github-forge
    remote: https://github.com/owner/notes.git
    tokens: {read: ro_token, changes: gh_token, report: gh_token}
    report: true
```

The forge whose `identify` claims the code's remote is the code
forge: proposed changes come from it. Every forge with `report:
true` gets statuses and comments, so a mirror can show results too.
Without a forge, branch, timer and manual runs work, and a
`pull_requests` trigger is refused when the pipeline is generated.
