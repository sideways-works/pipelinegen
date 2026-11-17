# Secrets

The pipeline needs one GitHub token, and the config
names the Concourse var that holds it (`github.token`), never the token
itself. Any other secret a task needs is declared by name, per kind of
task, and reaches only the tasks of that kind.

## What the token is for

- cloning the repository and fetching the commits a run tests;
- reading and writing pipelinegen's hidden refs: plans, run state, lists,
  the plugins' answer cache, locks;
- posting commit statuses and the pull request comment;
- downloading pipelinegen's and the plugins' releases.

The least that suffices is a fine-grained token for that one repository
with contents read and write (the hidden refs are written with it), commit
statuses read and write, and pull requests read and write (for the
comment). Plugins in private repositories need contents read on those.

Your task files don't get the token; pipelinegen's own steps that write
refs or statuses do, as the
[table of steps](../contracts.md#commands-the-generated-pipeline-calls)
lists. A batch task without a token still reports its slices:
pipelinegen keeps their states in the batch's result, and the step after
the batch writes them. Each slice then shows on GitHub when its batch ends
rather than as it ends.

`pipelinegen pipeline --vars` lists every var the written pipeline
references, so you know what to pass to `fly set-pipeline`.

## Secrets your tasks need

`params` are plain values, given to a suite's prepare, batch and
whole-suite tasks alike, and they can't hold a var reference: the config is
refused if one does. A secret goes under `secrets` instead, which maps an
environment variable to the name of the var that fills it, for one kind of
task:

```yaml
suites:
  e2e:
    secrets:
      prepare: {REGISTRY_PASSWORD: registry_password}
      batch: {STRIPE_KEY: stripe_test_key}
      whole: {}
```

A run can add to a suite's secrets under `runs.<run>.suites.<suite>.secrets`;
where both name the same variable, the run's wins. The planning step's
`concourse.planner.prepare` command takes its own `concourse.planner.secrets`,
and `concourse.planner.git_token` names the one of them git authenticates
with there. A variable can't be both a param and a secret, and neither may
start with `PIPELINEGEN_`, which is the tool's. The
[config reference](../reference/config.md) has every key.

When a task's own script runs git against a private repository,
`pipelinegen git-auth <VARIABLE>` prints the shell lines that make git ask
a credential helper for the token in that variable:

```sh
eval "$(tools/pipelinegen git-auth GITHUB_TOKEN)"
```

The lines name the variable and never hold the token, so a trace of the
`eval` or of git shows nothing secret.

## ssh keys

A remote reached over ssh takes a private key per purpose in place of a
token: `git.ssh_key` names the CI var that holds it, never the key. Use a
read-only deploy key for `read` and a separate key for `store`, which
writes the records and locks. One var for both works, but then a step that
may only read holds a key that writes.

Only the var's name is in the config, the spec and the generated pipeline.
The runner gives each pipelinegen step the key of each purpose it gives the
token of, in `PIPELINEGEN_SSH_KEY_READ` or `PIPELINEGEN_SSH_KEY_STORE`:
the read key alone to a step that plans a change's own tree, neither to a
task of the repository's. The tool writes a key it needs into a file of
mode 0600 in a directory of its own (0700, under the step's `TMPDIR`), and
removes it when the command ends, fails or is stopped. git's ssh gets the
file's path, never the key: the key is never echoed, put in an argument
or printed. ssh runs with that key alone, no agent, none of the machine's
ssh config, and only the host keys in `git.known_hosts`, checked strictly:
a server whose key isn't listed, or changed, is refused, never trusted on
first use.

A task of the repository's may never be given the store key as a secret:
the config is refused for it, in any run.

## Registry credentials

Cached images ([packages and cached images](packages-and-images.md)) live
in `images.registry`, and two vars may name its logins, each holding a
`username` and a `password`:

- `images.credentials`, the login that pushes, reaches only the job that
  builds a cached image. That job takes nothing from the repository but
  the config's version: it builds from the spec's packages on the pinned
  base. No suite job, planning job or task of the repository's holds it,
  in any run, so a pull request can't push.
- `images.pull_credentials`, a login that only pulls, reaches the step of
  a suite's job that fetches its cached image. That step runs in the
  registry resource's own image and runs none of the repository's code.
  Leave it out for a registry anyone may pull from. It may not be
  `images.credentials`.

The kit checks a runner's pipeline for this: neither var in a task that
runs the repository's code, and the pushing var in no job that runs any.
The packages and the registry come from the config the pipeline was made
from, like every key that shapes the pipeline: a proposed change's own
`packages` or `images` take effect once it is merged.

## What a proposed change's config can't change

A proposed change is a pull request, a change a connector lists, or a
branch's commit that pipelinegen can't show is on the branch. Its run
reads the change's own `pipelinegen.yaml`, so these keys come from the
config the pipeline was made from instead: `git.remote`, `git.ssh_key`,
`git.known_hosts`, `github.repository`, `github.token` and
`github.tokens`, `github.status_prefix`, `store.backend`, `store.branch`,
`bazel_diff`, `bazel.bazelisk`, `bazel.startup_flags`, the plugins' pins,
`concourse.planner.bootstrap`, each suite's `lock` (its repository,
name, slots, `per` and weight) and the time limit (`minutes`, in every
run) of a suite that has one, which its leases last, the `connectors`
except their `links`, `concourse.branch`, the runner's `config` block,
and the suites each run names (a change may add to them, never take one
out). Together they say where a run reads and writes, which credential
serves which purpose, which binaries a step runs, where the pipeline's
steps run, which suites a run waits for and how much of a shared machine
a run takes: a change can't raise its own slots
or lease on a lock it shares with other runs. The lock steps read them
too, so a lease is taken as the trusted config has it. The pipeline hands them to every step as
`PIPELINEGEN_TRUSTED`. A change that edits one is planned with the
repository's value, and its plan names the keys it edited.

Whether a run is proposed comes from its trigger, never from the config.
A pull request whose config calls its run a branch's is refused. A
branch's run is trusted only while its commit is on the branch as the
trigger's checkout fetched it. A commit the branch was rewritten past, a
checkout without the branch, or one too shallow to relate the commit to
the branch runs as proposed, and the plan says why ("this commit is no
longer on main"). A git resource's checkout of the branch's tip is on the
branch at any depth.

Some things this rule can't cover. `PIPELINEGEN_TRUSTED` is only as
trustworthy as the process that starts pipelinegen. The steps the
pipeline starts itself (planning, the tree, the reports) get it intact. A
batch task runs the repository's own scripts, and in a proposed change's
run those scripts are the change's, so they can unset or rewrite the
variable. Such a task therefore never holds a token that writes: no store,
report or changes token, at most one that reads. A pipeline made by an
older pipelinegen passes no `PIPELINEGEN_TRUSTED`, and its proposed runs
say "this pipeline was generated by a tool that does not pin where runs
read and write: regenerate it" until the pipeline is made again.

## Where it never goes

- to another host than the repository's: with the `github` section, a
  store whose remote isn't on the trusted git host (github.com, or
  `PIPELINEGEN_GIT_URL`'s) gets no token, whatever a config names;
- into a URL or a command line: pipelinegen's git takes it as an HTTP
  header through its environment, curl as a header that a redirect to
  another host drops;
- into a log: no script traces its commands or prints its environment, git
  runs without any `GIT_` variable of the worker's (a `GIT_TRACE_CURL`
  would print the header), and every error pipelinegen prints has the
  token masked, in its plain and its base64 forms;
- into a plugin: plugins run with a small environment (the path, home,
  locale, temporary directory, where Go is, and their declared caches,
  with Go kept off the network) and never see the token;
- into Bazel, bazelisk or bazel-diff: they run with the step's environment
  less every purpose's token (`GITHUB_TOKEN`, `GH_TOKEN`,
  `PIPELINEGEN_<PURPOSE>_TOKEN`), the ssh keys and every other
  `PIPELINEGEN_` variable. The one thing kept is the planner's declared git
  token (`concourse.planner.git_token`), which Bazel's fetches of private
  modules need; Bazel's repository rules can read it, so it must be a token
  that only reads;
- into a git hook, an fsmonitor or a filter: every git command of the
  tool's runs with `core.hooksPath=/dev/null`, `core.fsmonitor=false` and
  LFS's smudge off (`GIT_LFS_SKIP_SMUDGE=1`), and in a pipeline's step with
  no system or global git config (`GIT_CONFIG_NOSYSTEM`,
  `GIT_CONFIG_GLOBAL=/dev/null`); on your machine your own git and ssh
  set-up keeps working. The planner's and the helpers' images need no
  git-lfs unless your tree does.
- into the stored pipeline: the `pipeline` job doesn't set the pipeline
  unless `concourse.self_set` is on, and then passes no variables, so the
  pipeline keeps `((name))` references. Variables given to
  `fly set-pipeline -v` are stored with their values and a `set_pipeline`
  step would print them in its diff: on a Concourse without a credential
  manager or `var_sources`, leave `self_set` off and set the pipeline by
  hand when the `pipeline` job says it is stale.

Keeping the token out of a program's environment is hygiene, not a
boundary: a process of the same user can read another's environment
under `/proc`, and a step's shell holds what the step was given. On Linux
pipelinegen marks itself undumpable, so its own `/proc` entries can't be
read that way; that hardens, it protects nothing the step's shell holds.
What protects a token is not giving it to a step that runs a proposed
change's code: with tokens by purpose (`github.tokens`), the step that
plans a pull request holds the read token alone.

pipelinegen holds the pipeline to that. A step that plans a proposed
change refuses to run when it finds one of the tool's own variables of a
purpose that writes (`PIPELINEGEN_STORE_TOKEN`, `..._REPORT_TOKEN`,
`..._CHANGES_TOKEN`, the store's ssh key), naming it: the pipeline was
generated wrong. pipelinegen can't know a token's scope, so for
`GITHUB_TOKEN` and `GH_TOKEN` it goes by what the config declares. With
tokens by purpose, one of them in such a step refuses it, unless the
planner declares it as its git credential: then the plan says once "the
planning task holds GITHUB_TOKEN as its declared git credential: make sure
it can only read", and `pipelinegen check` carries the same as a note. Give
the planner the read token there:

```yaml
github:
  repository: owner/repo
  token: github_token
  tokens: {read: github_read_token, changes: github_token, report: github_token, store: github_token}
concourse:
  planner:
    secrets: {GITHUB_TOKEN: github_read_token}
    git_token: GITHUB_TOKEN
```
 With one token, every step holds it, so a
pull request's plan says first, and `pipelinegen check` warns, that the
pipeline gives a pull request's code a token that can write; the next
release refuses it. Move to tokens by purpose, the read one a token that
only reads.

The same goes for your own tasks. In a run that takes pull requests, a
suite's prepare, batch and whole-suite tasks run the pull request's own
scripts, so a secret there may not be a token that writes: the single
token, or the changes, report or store token. With tokens by purpose the
config is refused for it. With one token, `pipelinegen check` warns and
the run's plan says first "task e2e/prepare is given github_token, a token
that can write, in a run that executes a change's own scripts: give it a
token that only reads (github.tokens.read)", and says it will be refused.
Give such a task the read token under the same variable:

```yaml
suites:
  e2e:
    secrets:
      prepare: {GITHUB_TOKEN: github_read_token}
```

A run on a branch alone runs only the branch's scripts, and may give its
tasks a token that writes.
