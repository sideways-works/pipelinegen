# Changelog

Every release of pipelinegen, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0.0 a
minor version may change the config or the protocol.

## [Unreleased]

## [0.12.0] - 2026-11-26

### Added

- `memory.wait` makes the pipeline's own planning steps (a branch's plan, a pull request's first step) and bookkeeping steps (a batch's end, a suite's end) wait for their memory too, asking `memory.broker` when it names one, before their heavy part: each trusted run's such step records its task's peak by step, and a step expects what its recent peaks predict, else what the same command's steps of other runs use, else `memory.steps.planning` (4096 MB) or `memory.steps.bookkeeping` (1024 MB). Such a step waits at most a third of what its limit leaves, then starts anyway, since a plan or report that failed for memory would stop its run
- `cpu.wait` (top level, or a suite's own): a batch also waits, in its wait for memory, until the worker's CPU pressure (`/proc/pressure/cpu`, the larger of `some avg10` and `avg60`) is below `cpu.pressure` (40%) and, with `memory.broker`, until the broker's cores leave room for its predicted cores: a trusted run's suite end keeps each slice's cores (its CPU seconds over its seconds) beside its peak, and a batch is predicted the most any of its slices should keep busy, else `cpu.cores` (1). The broker counts `--cpu-factor` (1.25) cores per core of its machine and holds back asks for CPU at or above `--cpu-pressure` (40%); a grant's cores are a lease of the batch's expected time half again, which a `slice run` batch releases at its end. A batch still short of CPU leaves its slices not run and says so, and under the top-level `cpu.wait` the pipeline's own steps wait for their cores (below), then start anyway. Upgrade each worker's `memory serve` before turning `cpu.wait` on: an older broker grants an ask for CPU as one for memory alone
- `pipelinegen wait prepare` and `wait whole`, first in a suite's own prepare or whole-suite check task: under the suite's `cpu.wait` or `memory.wait` the task waits while the worker is under CPU or memory pressure, asking memory.broker when it names one, and starts anyway when its wait ends; `slice prepare` waits the same way for a suite with steps
- `pipelinegen slice release`, in a task file's exit trap: hands back the cores the worker's broker granted the batch's `slice memory`, so they count no longer once the batch is done; a finished task's container can stay on the worker a while, so neither the probe nor the lease freed them in time
- `memory serve --cpu-settle` (15s): grants of cores go that far apart, so the CPU pressure each reads shows the batch granted before it; asks that fit at once used to start together, on one reading of the pressure
- Under `cpu.wait` the pipeline's own steps ask `memory.broker` for their cores as batches do: a planning or bookkeeping step asks for what its recent cores predict, else those of the same command's steps of other runs, else `cpu.steps.planning` (2) or `cpu.steps.bookkeeping` (0.5), and a suite's prepare or whole-suite check, under the suite's own `cpu.wait`, for its suite's recorded prepares' or `cpu.steps.prepare` (2) or `cpu.steps.whole` (2). The cores are leased for the step's recorded seconds half again (else 20 minutes for a plan, 5 for a bookkeeping step, 30 for a prepare or a check), never past its time limit, and a step that ends in pipelinegen releases them at its end; `wait prepare` and `wait whole` end before the task's work, so their cores count until the broker's probe finds the task gone or the lease ends. Each trusted run's step keeps its cores (its task's CPU seconds since its wait over its seconds) and its seconds beside its peak, and `slice prepare` now keeps them too
- A suite job's `tree` (when it runs something), `batches` and `slice bookkeeping-files`, and a whole-suite check's `report collect` and `report whole`, wait for their memory and cores under the top-level `memory.wait` and `cpu.wait`, as the other bookkeeping steps do; `slice bookkeeping-files` opens no store, so it asks for its kind's defaults and keeps nothing
- The broker books a step that starts anyway: a step's ask whose wait ends is granted then ("granted anyway", with what the broker read), its memory and cores counted and leased, so the asks after it see the work that started under pressure; a batch's is refused as before. A step's ask goes ahead of the first in line, which holds batches back for `--first-for`, when the worker has room for it, and its grant of cores starts no `--cpu-settle`. An older broker refuses a step's ask when its wait ends and the step starts unbooked: upgrade each worker's `memory serve` with the pipelinegen the pipeline runs
- `memory serve --probe-every` (30s): the broker asks whether the task of each grant holding cores is gone from the worker's network (no route leads to its address), and ends the cores of one gone on two probes in a row, so a task file's batch, which asks from `slice memory` and releases nothing, holds its cores only while its task runs and no longer until its lease ends

### Changed

- A suite's status counts the slices not run for room as "not run (worker short of memory or CPU)", since a batch may now wait for either

### Fixed

- A slice's recorded cores are its CPU seconds over the time they were read over: a driver's own measure of the slice, without a bundle's compile, made some slices' cores many times the machine's, and a batch predicted from them waited for or held the whole worker; recorded cores above the machine's count for nothing, and an ask for more cores than the broker counts holds all of them, no more
- A write to a branch store (`store.backend: branch`) names the tip it was made on when it pushes, so a remote whose answer briefly leaves out the branch while another push replaces it (seen with git's files backend on btrfs) costs the write a replay; it used to push as if creating the branch and fail with "a push needed an object the store never had", since that pack holds every file of the tree, most never fetched

## [0.11.1] - 2026-11-25

### Fixed

- A slice's command and a driver plugin run in a session of their own, without a terminal: a step that asked the terminal (git for a password, say) used to stop its whole process group when pipelinegen ran under one, as it does in a Concourse task, and the slice then waited forever with its timeouts stopped too; it now fails at once

## [0.11.0] - 2026-11-25

### Added

- `runs.<name>.passed_within` (off by default): a full run (`select: all`) leaves out a slice that passed in it within the period and that nothing changed since reaches, under the `passed-since` narrowing; suites with whole-suite checks run whole, and a slice that ran and passed still replaces its list. A full run following every commit no longer runs every slice again after a round that failed in one place
- Store plugins: with `store.backend: plugin` and `store.plugin` naming a plugin entry of the new kind `store`, a plugin keeps the records (plans, run and slice states, rounds, recordings and lists, the plugins' answers) instead of the repository's refs, through the calls `store.list`, `store.get`, `store.read` and `store.write`, its credentials in `PIPELINEGEN_STORE_*`. `store.credentials` names the CI vars of a key that writes and one that only reads, which the spec hands a runner as `store`; a runner reports `store-keys` to say it gives each step the key it may hold, and a runner without it is refused. Every store, the refs and a branch included, is held to one conformance suite of the promises the core relies on (docs/writing-plugins/store.md)
- `pipelinegen why --cost`: what a plan's selection cost, per suite the changed units and the junctions on the way that selected the most slices, and how many each selected alone, so a change or a link that selects widely shows at a glance
- A runner capability `own-settings`: a runner for a CI system other than Concourse takes its settings from its own config block, and a config whose runner reports it needs no `concourse` section; the spec's `ci` is then empty
- `memory.wait` (top level, or a suite's own): a batch waits for the memory its slices' recorded peaks predict until it fits, for as long as it could still finish within its time limit, instead of at most 5 minutes; a task file's batch that never calls `slice memory` waits at its first `slice start`, for its prediction less what its set-up holds. With it on, pipelinegen rather than Concourse's task limit decides how many batches a worker runs at once. The plan's log says per suite how many selected slices have a recorded peak
- `pipelinegen memory serve`, a worker's memory broker, and `memory.broker` (top level, or a suite's own): a waiting batch asks the broker of its worker, which grants the asks in the order they came, each once the free memory less what it granted lately covers it, so batches starting at once no longer all see the same room
- `memory serve --cgroup <path>`: a broker on the worker's host reads the worker container's limit, not the host's, and `--first-for` (10 minutes): the first ask in line holds back later asks the memory covers at most that long, so one batch asking for most of the worker can't hold the others back for its whole wait. A request asking for less than nothing, a wait over a day or a name over 256 bytes is refused, and at most 512 connections are served at once. A task asks once at a time and holds one grant at most, its new one replacing its last, and a grant counts at most `--max-grant` (16384 MB)
- `pipelinegen memory sample --suite <suite>`: runs a sample of a suite's slices on this machine, each alone in its image, measures their peaks as CI does and records them with 25% headroom, so a suite can wait for memory before CI measured it

### Changed

- A slice without a recorded peak is predicted at the 90th percentile of its suite's others, not their median
- A change to `memory` or a suite's `memory` in the config selects nothing: when a batch starts is no test's to observe

### Fixed

- `passed-since` compared a slice only from the newest two commits at which any slice passed, so on a red branch whose checks passed on every commit, a suite whose slices last passed further back ran whole: each suite now counts its own newest two (four commits at most in all)
- Reading the lists asked git once per live recording whether its run came before the lists, though a run's recordings share the answer, and read and parsed the recordings the lists then dropped whole. A plan reading tens of thousands of live recordings, as one does while a branch stays red and its lists aren't replaced, now asks once per run and reads only what it keeps

## [0.9.0] - 2026-11-24

### Added

- The core narrowing `linked-imports` (off from 4): an import a package gains or loses no longer changes the package whole. A slice that links the package runs only when, tree by tree, it links in one tree only what the imported packages run at start-up: their inits, variables whose initialisers may act, the modules they bring. A slice the change selects through a changed declaration still runs. An import of a package that runs nothing at start-up ran every slice linking the importer; past 64 such things the package changes whole, as before. `pipelinegen why` names the slices it left out

### Changed

- A continuous run's round whose plan fails records it, so the next commit's build plans at once instead of waiting for the round to count as dead (`limits.plan` and its margin, 2h10m by default). A build of the same commit that starts again is waited for as before
- `passed-since` leaves out a slice that passed since the base in a suite whose setup's change runs it whole, unless the suite has whole-suite checks: what changed from its pass to the head would have reached it, setup and all. Such a suite ran every slice on each commit of a red branch. The slices that still run give the setup's target as their reason

## [0.8.1] - 2026-11-23

### Fixed

- `passed-since` and a pull request's carry-over counted no pass of a test file's slice: a batch records such a slice under its file's name, and the plan names it `@pipelinegen_deps//slices:<file>`, so the two never matched and every reached test file ran again on each commit of a red branch, though it had passed since. Only slices of Bazel targets were left out

## [0.8.0] - 2026-11-23

### Added

- A plan has a `version`, 1, and its format is a JSON Schema, docs/reference/plan.schema.json, held to the plan's Go types by a test
- Each suite of a plan that keeps a durations file has an `estimate`: the recorded seconds of the slices it selected and of all of them, a slice without a recorded time at the median
- `plans.s3` copies each plan a trusted step writes to a bucket of AWS S3 or an S3-compatible service (SeaweedFS, MinIO), gzipped, under `<prefix>/<owner>/<repo>/<yyyy>/<mm>/<dd>/<run>/<change>/` with a pointer per head commit under `heads/<head[0:2]>/<head>/<run>/<change>/`, its metadata naming the run, the change, the commits and the counts; `plans.keep` deletes the copies older than it. The key comes in `PIPELINEGEN_PLANS_ACCESS_KEY_ID` and `PIPELINEGEN_PLANS_SECRET_ACCESS_KEY` to the steps that write a run's plan alone, and the runner's spec names its vars as `plans`; a bucket that fails is a warning, never a failed plan. See docs/how-to/keeping-plans.md

### Changed

- A release's NOTICE names the licence its LICENSE holds, the Apache License 2.0 or the GNU AGPL 3.0 (version 3 or any later version), and the licence of each part of the family's code it includes, read from that code's nearest LICENSE; a LICENSE that is neither is refused. A NOTICE under the Apache License reads as before

## [0.7.2] - 2026-11-21

### Changed

- A failure's output is kept up to its last 400 lines and 32 KiB, up from 15 lines and 1,500 bytes, so a timed-out test's goroutine dump reaches the run's records and the step's log of slices that did not pass; a step's log says up to 64 KiB of them. A pull request comment still shows each failure's last 15 lines

## [0.7.1] - 2026-11-21

### Fixed

- A suite whose tests passed no longer turns `error` because their recordings held nothing (tests that ran no code a correlator sees, such as a package of SQL files alone). Such slices are bound to everything and the suite passes, counting them as not recorded; a suite errors only when its correlators found no recording at all

## [0.7.0] - 2026-11-20

### Changed

- Breaking: the plugin kind connector is now forge. A config's section `connectors` is now `forges`, a plugins entry pins a forge with `kind: forge`, and the forges are `forge/git` and `forge/github` of the plugins repository, programs `git-forge` and `github-forge`, which pipelinegen pins at v0.5.0 by their `SHA256SUMS`. A config that still says `connectors` or `kind: connector` is refused, saying the new name, and so is a plugin whose `describe` answers kind connector. For runners, the spec's run field `connector` is `forge`, and the capability `connector_changes` is `forge_changes`: concourse-runner v0.6.0 speaks both
- pipelinegen runs no plugin whose kind isn't its own (sbom, correlator, runner, driver, forge): a config entry, a `describe` answer, a plugin's manifest and a zip naming another kind are refused, as another system's plugins share some names
- Plugin manifests (plugkit v0.6.0): `build.run` may be a list of commands, run in order with no shell, `build.copy: true` builds in a copy of the files git tracks, and an untagged build's `{version}` is `0.0.0-dev+<commit>`, so a manifest needs no shell to stamp it ([the manifest](docs/reference/manifest.md))

## [0.6.0] - 2026-11-20

### Added

- A pull request's build carries over what its last build passed: while `passed-since` is on (levels 1 to 3), a slice that passed in the newest earlier build of the same run and pull request with its tests run, or that build carried over, isn't run again when nothing from the tree that build tested to the tree this one tests reaches it. That change holds what the target gained in between and what a rebase or force-push rewrote: the trees are compared, not the history. The slices that failed, errored, didn't run or weren't selected there run, and so do those the change reaches. A build whose tree can't be made again, or whose records are gone (`keep.runs`) or unreadable, carries nothing over, and the plan says why. The level is the stricter of the pull request's config and its target's. The suite's and the run's statuses say "`<n>` carried over from `<commit>`", and `pipelinegen why` names the build and how many slices of the suite it carried ([a pull request's later builds](docs/how-to/narrowing.md#a-pull-requests-later-builds))
- A `merged_target` run's plan records the pull request's commit it merged (`merged`), which its second step takes from the trigger, so a later build can make the tree it tested again

### Fixed

- A branch's `last_passed` run honours `narrowings.passed-since`: an `off_at` the config sets switches `passed-since` off from that level, as it does a pull request's carry-over. Before, the branch's run kept the core's level 4 whatever the config said. Both take the stricter of the change's config and its base's

## [0.5.0] - 2026-11-20

### Added

- `pipelinegen plugin describe <dir|zip>` prints what a plugin's `manifest.yaml` says, from its directory or its zip: name, kind, program, runtime, platforms, description, what it implements and requires, and its permissions
- A plugin's `manifest.yaml` may name its `runtime` (`node`, or `"node >=26"` with a range; none is a native binary) and its `permissions`, each `{name, description, default}` with `name` as `area:action` and `default` one of `display`, `viewer`, `operator`, `maintainer` and `admin`
- A plugin's `manifest.yaml` may name `description`, `logo`, `implements` and `requires` (each `"<name> <semver range>"`), and a plugin of kind `meta` is schemas alone: `schemas` and `ack_timeout`, no build and no program. Its build's command and environment may use `{commit}` and `{version}`. `pipelinegen plugin build` fills `{commit}` with the commit checked out in the plugin's repository and `{version}` with `--version vX.Y.Z`; a plugin pinned by `commit` is built with its commit and no version ([the manifest](docs/reference/manifest.md))
- A plugin no longer needs a release. Its directory may hold a `manifest.yaml` naming it (`name`, `kind`, `program`) and saying how to build it: an image pinned by digest, a command with `{out}`, `{os}` and `{arch}`, its environment, its platforms (`os/arch`, or `[any]` for one build), and the `files` and `assets` that go with the program. Unknown keys and an image without a digest are refused ([the manifest](docs/reference/manifest.md))
- Config: `plugins[].zip: {url, sha256}` pins a plugin packed as a zip, beside `release` and `commit`; an entry gives exactly one of the three, and needs no `repository` with a zip. The zip is downloaded over https, checked against its sha256 before it is opened, and unpacked into the user cache under that sha256, once per machine. It must hold its `manifest.yaml` at its top level, and is refused with a path that leaves its directory, a symlink, a file that is no part of the plugin, an executable besides the program, or more than 1 GiB unpacked. The manifest's kind must be the entry's. A `tools/plugins/<name>` never stands in for a zip pin. The spec's tools carry it as `zip` ([building and hosting a plugin as a zip](docs/how-to/plugin-zip.md))
- `pipelinegen plugin build <dir> --zip <out.zip> [--platform os/arch]...` builds a plugin from its manifest in a container (docker or podman), with the repository read-only, no network unless the manifest asks for one, and `GOFLAGS=-trimpath`, and packs every platform into one zip, `manifest.yaml` unchanged at its top level. It writes `SHA256SUMS` beside the zip and prints the zip's sha256; the same build packs to the same bytes
- `pipelinegen plugin add --zip <url> [--sha256 <hex>]` pins a zip: it downloads and checks it, reads the name and kind from its manifest, and writes the entry
- A command given its own `--cache`, as a pipeline's steps are, keeps the plugins it builds from a commit or unpacks from a zip under `<cache>/plugins/<key>/`, where `keep.cache` counts them and trims them with the rest; without one they stay in the user cache beside the releases

### Changed

- A plugin pinned by `commit` that `tools/plugins` lacks is built from the `manifest.yaml` in its directory at that commit, for the platform that asks, and kept in the user cache under a key of its repository, commit, path, manifest, platform and build image, so a machine builds it once; before, the command failed with the commands to build it by hand, as it still does for a commit with no manifest or one git can't fetch. A `tools/plugins/<name>` that is there still runs instead. Release pins, and the digests every existing pin gives its plugin's unit, are as before
- Config: a plugin's `name` may not start with a dot or hold a slash. It names the plugin's directory in a cache, beside the directories of the plugins built or unpacked there
- `pipeline` and `plugins claims` find their plugins as planning does: the copy in `--plugins` when it is there, otherwise fetched, built or unpacked by its pin. Before, they only read `--plugins`, and a runner or sbom missing there failed

## [0.4.2] - 2026-11-19

### Added

- `slice bookkeeping-files --root source --out bookkeeping` writes what a batch's end reads of the tree under test, the config, the same bytes every time, so a runner can give a batch's end placed on another worker `bookkeeping` instead of the whole tree. A batch's end without the tree marks can't tell a slice the batch left unobserved, saying why, instead of failing its observe; a recording run of a suite whose batch is its own task file still needs the tree at its ends, which the kit checks ([contracts](docs/contracts.md#commands-the-generated-pipeline-calls))

### Changed

- A batch's correlators claim their files in the tree only when a collect or an observe first needs them, instead of at the start of every step of the batch: a batch's end whose slices the batch observed already reads no tree

## [0.4.1] - 2026-11-19

### Changed

- The built-in GitHub and git connectors come from the plugins repository: `connector/github` and `connector/git` of [pipelinegen-plugins](https://github.com/sideways-works/pipelinegen-plugins) at v0.4.0, in place of v0.1.0 of their own repositories. A config without a `plugins` entry of kind `connector` fetches these, and the spec's tools name them with their `path`. An entry that pins a connector itself is used as before

### Fixed

- A record written again within the same second as the compaction that moved its older copy into the run's ref (a suite's end mark, a slice's state on a rerun) is the one the next compaction keeps. Its marker ties with the older copy's, as markers count whole seconds, and readers already took the newer copy then; the compaction kept the older one, so the record went back to what it was before. Since a suite's end mark goes into the suite's own ref, a suite ended twice in one second showed it
- `release patch` pushes its tags only after the build passed its checks (changelog, NOTICE, programs), and a build that fails takes back the tags the run made: a tag pushed for a release that then failed could never be released, as a pushed tag never moves.

## [0.4.0] - 2026-11-19

### Added

- In a pipeline, `plan`, `tree`, `batches`, `report batch-end`, `report suite`, `report whole` and `report collect` end with a line on stderr saying where their time went: the total, each stage's time (the store, each plugin, each connector, Bazel, bazel-diff, `other`), and how many git commands reached a remote and how long they took ([troubleshooting](docs/troubleshooting.md#a-step-is-slow)). On your own machine nothing changes
- `plan --from <dir> --emit <dir>` also writes piperun's emission of the plan (`emit.json`): per suite its prepare, a pool of its selected slices longest first, its selected whole-suite checks and its end, named `<suite>-prepare`, `<suite>-slices`, `<suite>-whole-<check>` and `<suite>-end` (functions of the `plugin` package, for the piperun runner), with the plan beside it as the build's record (`plan.json`) and each pool's slices with their predicted peaks in `--pools` (default `pools`). An emission over piperun's limits (500 instances, 2,000 items a pool, 48 KB) fails the plan before it is written, naming the limit; a plan written before is emitted again without the slices that passed
- `slice run --feed <dir>` runs a piperun session of a suite's pool: after the set-up it reads the chunks in order until the end, runs each slice as a batch does, and writes a result line under 4 KB for its start and its end (outcome, summary, failure files copied within 2 MB a slice and 50 MB a session, duration, CPU and peak), reading the next chunk only once every item of this one has its line. A slice the worker's memory can't fit beyond what the session holds is handed back with a requeue line before its start; `PIPELINEGEN_LIMIT`, else the suite's minutes, limits each slice, not the session; the batch is the session's number. A session started again keeps its journal and takes nothing it left unreported as its own
- `report flush --confirmed <file> [--every <period>]` writes the ends of a session's slices piperun confirmed, from its journal, which it only reads; a flush written twice writes the same states. `report batch-end --confirmed <file> --requeued <file>` ends a session: it closes the confirmed slices, a confirmed one without an end as errored, and leaves the requeued ones open. Both lists are piperun's JSON lines (`{"key", "outcome"}`, `{"key", "reason"}`), a line counting only with its newline. A slice confirmed `stopped` errors saying a person stopped the build, and is neither flushed early nor handed on
- Drivers: `describe` may name `renders`, the CI systems a driver writes a suite's run step for in the system's own steps (`target`, a semver range of its `versions`, and the target's `plugins` the steps use with a range each), and such a driver answers the new call `render`: given the target and its version, the suite's step and set-ups, the shape (`pool` or `batch`), and the names of its secrets, never their values, it answers the target's `steps` and their `marks`, `results` and `caches`. `plugin.RenderTarget.Choose` is a runner's choice between such a render and the fallback through `slice run`, with a notice naming the first condition that failed. The conformance kit checks the ranges, and calls `render` for each target at the versions and shapes the fixture's `driver.render` names. No driver renders yet, and a driver without `renders` is checked as before
- Config: `runs.<name>.runner` names the runner plugin whose pipeline holds the run, by default the only runner configured, so a config with one runner needs no change. A config may now have several runners, each run naming one; it is a trusted key, so a proposed change that moves a run to another runner is planned with the run where it was. `pipelinegen pipeline --for <runner>` writes that runner's pipeline: the spec's `runs` are the runs it owns, the new `other_runs` names the rest, and the new `runner` is the runner's own name, for the `--for` of its pipeline's own job. With one runner the spec, the trusted keys and the pipeline are as before
- Config: a plugin's `repository` may be any git URL (`https://github.com/owner/name`, `git@github.com:owner/name.git`, other hosts) as well as GitHub's `owner/name`, which stays as it is; messages name it as the config writes it. `plugins[].path` names a plugin inside a repository that holds several: its releases are tagged `<path>/vX.Y.Z` and its program is named by the path (`driver/go` holds `go-driver`). An entry `{repository, with: [...]}` groups plugins of one repository, each entry under `with` taking that repository; a group reads exactly as its entries written one by one. Configs without these keys read, plan and generate as before. The built-in connector pins and the spec's tools carry a `path` too, empty for now
- `pipelinegen plugin add <git-url> [<path>...]` pins plugins nobody has to look a checksum up for: for each path it finds the newest release that is no prerelease (`<path>/vX.Y.Z`, or `vX.Y.Z` without a path; `--version` picks one), downloads its `SHA256SUMS` and every file of the plugin it lists, checks each, reads the name and kind from its `plugin.json`, and writes the pin by `sums` into the repository's group, made when there is none. `pipelinegen plugin upgrade [<name>...]` moves pins to the newest releases the same way (`--pre` takes candidates), keeping each pin's form and printing old and new versions; a plugin it can't move is named with the reason and left as it was while the rest move, and the command then exits 1. Both edit `pipelinegen.yaml` in place, keeping its comments and layout, and write nothing when a plugin can't be pinned. Releases are listed through GitHub's API (with `GITHUB_TOKEN` when it is set) or GitLab's
- `pipelinegen plugin new <kind> <name> [--into <dir>]` writes a new plugin from the template the plugins repository releases for its kind (`templates/<kind>/` at the newest tag `templates/vX.Y.Z`, fetched with git), its module path, its program and every reference to its own directory renamed. `plugin test` is now a subcommand among these, and runs as before
- Releases: a Go module with `./cmd/NAME` releases every other main package under `cmd/` beside it, each program's four platform files in the same release and `SHA256SUMS`, a program named for a plugin kind with its own `NAME.plugin.json`, and one `NOTICE` for all of them. A repository with one program releases the same files as before
- Releases: the release command releases a module in a directory of a repository, a repository of several plugins: `build`, `check`, `patch` and `tagged` take the module's directory. It is tagged `DIR/vX.Y.Z` and a module nested in it `DIR/SUB/vX.Y.Z`, carries its own `LICENSE` and `NOTICE` or the repository's, names its program itself (its one program under `cmd/`, the one its module path names, or its package name), and its programs record the repository's commit. `check DIR REPO TAG` fails unless the published release is byte for byte a fresh build of its tag. A build leaves out a `go.work`, and a Node package of an npm workspace takes its bundle's versions from the workspace's lockfile. A repository's root module releases the same files as before

### Changed

- A suite's start and end marks go into a ref of the suite's own, `refs/pipelinegen/batch/<run>/<change>/<c12>/<suite>/_suite`, instead of the run's one ref, and the run's batches and suites move into the run's ref once, by the suite's end that finds every suite ended, instead of at every suite's end. Suites of a round that start or end together no longer write one ref in turn: six starting at once push 6 times instead of 21 and the last began after 7.8s instead of 20.7s, six ending at once took at most 14.4s instead of 23.2s, with 0.6s a round trip. A release before reads the new refs as a batch's and sees every record; a round a suite never ended keeps its suites' refs until the sweep takes them with the run ([refs](docs/internals/refs.md))
- In a pipeline, `plan` and `unplanned` shut down the Bazel server they planned with when they end, instead of leaving it to idle out for ten minutes in the step's container ([planning](docs/internals/planning.md#the-cache-a-step-leaves)). A step that started no server starts none
- A write to a ref the step has already seen (its run's ref after reading the plan, or after a try that lost a race) fetches it without asking the remote first whether it exists, which a fetch of a ref gone since still asks. Six suites of a round writing their start at once now take 60 round trips instead of 81, and the last of them began after 17s instead of 22s with 0.6s a round trip; the writes still go one after another, as they all change the run's ref
- Reading the store takes fewer round trips: listing a run's records fetches its grouped refs and the older layout's refs in one fetch instead of two, finding one record (a plan, a suite's end) is one fetch instead of an `ls-remote` and a fetch, and a write to a run whose plan the step has read no longer asks the remote which layout the run has. On a commit of this repository with a local remote that is 31 to 20 round trips for `plan`, 9 to 6 for `tree`, 6 to 4 for `batches`, 9 to 6 for a batch's end and 15 to 13 for a suite's end. What is read is the same
- The kit checks that a runner fetches a tool pinned by `sums`: it writes the pipeline with a stand-in plugin, and again with the runner itself, pinned by the checksum of a `SHA256SUMS` it places beside the tool as `tools/plugins/<name>.SHA256SUMS`, with no `sha256`, and each task it runs must reach its call of the tool or exit 0 with the list and the tool still matching. A runner that checks only a `sha256` passed the kit before, though every real task would fail. Run again with the tool changed so that it no longer matches its line, each task that fetches the tools itself and called the tool must refuse it and call nothing, so a runner that keeps a tool with no `sha256` unchecked fails as well.
- The kit runs a runner's task that calls pipelinegen through a program of the runner's own instead of a script naming the calls: each task marked as the tool's step, in its job's order, offline, with the runner in `tools/plugins/<name>` and a stand-in for the tool that notes each call, so its calls face every check a script's do. The kit's fixture spec pins a runner, made the one under test, and a variant gives the planner a prepare command with a git token
- The plugin protocol's parts that know no kind (calls and their exit codes, the connector exchange, pinned downloads, the container runner, the kit's harness) come from the module `github.com/sideways-works/plugkit`. `plugin` names its types as its own, so plugins build unchanged, and nothing a plugin reads or writes changes
- The docs, the book and the examples pin pipelinegen's plugins from the plugins repository, `github.com/sideways-works/pipelinegen-plugins`, in one group by directory (`sbom/go`, `driver/go`) at v0.4.0, each by the checksum of its `SHA256SUMS`; the Concourse runner keeps its pin until its own v0.4.0. A plugin kept in `tools/plugins/` and pinned by `sums` has its release's `SHA256SUMS` beside it as `<name>.SHA256SUMS`, which the tutorial downloads with it and the book checks with it. The example's coverage build links go-correlator's `cover` package as `github.com/sideways-works/pipelinegen-plugins/correlator/go/cover`
- Releases: a `NOTICE` names the plugins' own code it includes (a plugin's Go module, or a package such as `@sideways-works/ts-sbom`) as the plugins repository's, `github.com/sideways-works/pipelinegen-plugins`, where it lives
- The proof plans with go-sbom and go-correlator v0.4.0 from the plugins repository: `ci/proof/pins` names a release by its tag, `sbom/go/v0.4.0` for a plugin in a directory, and `--newest` takes the newest release of each plugin's directory instead of the repository's latest

### Fixed

- The pipeline's release job succeeds for a tag released already when the release's files are a fresh build's, byte for byte, instead of failing for want of a nightly on a commit released by hand.
- The kit's placement check lets a placement add the gets a moved task needs on its worker, a copy of a get of its job under a name of its own that never triggers, named by the task's `image` and `input_mapping`, so concourse-runner's `bookkeeping` rules no longer fail it. Anything else that differs still fails it, and the check now names where the pipelines differ.
- The command driver finds a bare command such as `bazelisk` on the step's PATH, which puts the tools first, rather than on pipelinegen's own, so a `run: command` step can name a tool the task fetched.
- The kit's placement check renders the pipeline without the placement the same way as with it, its tools at the checksums the kit places, so a runner that writes its tools' checksums into the pipeline no longer fails it.
- A trusted run (the default branch's own) whose config differs from its base's no longer runs what its config dropped: the base's config adds only the slices and whole-suite checks the head's config still has in each suite, and no suite the head's config removed, and the plan warns of each one left out (`<slice>: the base's configuration had it in <suite>; this commit's configuration excludes it, so it no longer runs there`). A suite whose base predated an exclude ran the excluded test until it passed, and a test that can't pass there kept it failing. A pull request keeps the whole union, so it still can't deselect its own tests

## [0.3.2] - 2026-11-18

### Fixed

- A trusted run (the default branch's own) whose config differs from its base's no longer runs what its config dropped: the base's config adds only the slices the head's config still has in each suite, and no suite the head's config removed, and the plan warns of each one left out (`<slice>: the base's configuration had it in <suite>; this commit's configuration excludes it, so it no longer runs there`). A suite whose base predated an exclude ran the excluded test until it passed, and a test that can't pass there kept it failing. A pull request keeps the whole union, so it still can't deselect its own tests

## [0.3.1] - 2026-11-18

### Added

- `run --slice <name>`, repeatable, runs only the named slices, by name or label, in every suite that has them: of the plan's selection, or of every slice with `--all`. A slice the plan didn't select runs too, and a line on stderr says so; a name no suite has exits 2 and names it
- Failure files: a result's `files` names what a failed test left to show why (a screenshot, Playwright's `error-context.md`, a trace), by paths from the result directory with a short label. A slice that failed or errored keeps them in its ref beside `state.json`, a passed one none; they go with the run's records under `keep.runs`, and `report collect` leaves them out. A path that is absolute, has a `..` part or leaves the result directory through a symlink is refused, and only regular files are read. Text files are masked for known secrets, binary ones kept as they are: a failure file can hold what a test printed, so it goes only to the store, whose readers are the code's, never to the comment
- Config: `keep.files.slice` (2 MB) and `keep.files.run` (50 MB) cap what failure files keep, each slice's and each run's; over a cap the largest go first, and the slice's state names each dropped file and why. A write the store refuses with them goes again without them
- `pipelinegen report files --run <run> --change <change> --out <dir>` writes a kept run's failure files into a directory, by suite and slice, and lists what each slice kept and dropped; the comment counts each failed slice's files and gives the command, and `why --run` says the same for its slice
- Drivers: a run request's slices carry `files`, the directory for their failure files, and an `end` may name them in `files`. The command driver passes it as `{files}`; the kit checks that an end names only regular files inside it, for a slice that didn't pass
- Memory: each slice's state holds the CPU it used and the batch's memory peak while it ran (`cpu_seconds`, `peak_mb`), measured from the task's cgroup: sampled every 250 ms by `slice run`, the batch's peak up to the slice's end for a task file. A trusted run's suite end keeps the last 10 peaks per slice in `refs/pipelinegen/peaks/<suite>`, and `batches` writes each batch's predicted peak (the most its slices should use, each the 90th percentile of its peaks and 10% more, a slice without any at the suite's median) to `batches/<n>/memory.json`
- A batch waits, before its setup, until the worker has its predicted peak free (`MemAvailable`, and what a cgroup limit above the task leaves) and memory pressure (PSI `some avg10`) is below 10%, for at most 5 minutes or a tenth of the suite's minutes, and logs the reading either way. A batch still short then runs none of its slices: they are `not run` with the figures ("the worker had 3120 MB free of 6600 MB predicted"), and the suite's status says `2 not run (worker short of memory)`. `slice run` does it itself; a task file calls the new `tools/pipelinegen slice memory` first. A suite with no peaks yet starts as before. `run` and `record` refuse such a suite on your machine, naming the figures
- A slice during which the OOM killer killed a process of its batch, or that failed naming `SIGKILL` under memory pressure, errors with "(out of memory)", and its peak counts as at least the room the batch had, so the next prediction rises
- The pull request sweep hands a head on again, after `missed_after`, when its run ended with no gap but slices not run for want of memory or crashed out of memory, so they run without anyone; the suite builds run only what didn't pass. A branch's next round selects them again, as it does every slice that didn't pass
- Config: `packages`, `suites.<name>.packages` and `concourse.planner.packages` declare the packages the steps need, by name or by name per family (`{name: postgresql-client, apk: postgresql16-client}`; families `apt`, `apk`, `rpm`). Before a step's command an install script finds the image's package manager (apt-get, apk, dnf, microdnf or yum) and installs only what is missing, with a download cache on CI. Task files run the repository's own scripts, so only a cached image brings them the packages. A change to the packages runs the suite's slices. See [packages and cached images](docs/how-to/packages-and-images.md)
- Config: `images` (`registry`, `credentials`, `pull_credentials`) keeps cached images: a suite's base, pinned by digest, with its packages installed, tagged with a key of the digest, the sorted packages and the install script. A runner with the `packages` capability builds each image once in a job of its own that alone holds the pushing var, and runs the suite's tasks in it. When the registry hasn't got the image or can't be reached, the tasks install on the base. The planner image is never cached
- `run` and `record` build the image with a suite's packages once in the local image store, as `pipelinegen-cache/<key>`, and run the steps in it; no registry is needed. With `--no-image` nothing is installed, and the packages the machine has under no command or installed package of their name are named
- `check` notes how each suite gets its packages on CI, a cached image or an install per task and why, and warns of task files that would run without them. `pipelinegen pipeline` and `check` say when the runner can't install packages
- Spec: `suites[].packages`, `planner_packages` and `images`, with the cached images' keys worked out by the tool, and the runner capability `packages`. A runner without it writes the pipeline it always did. Conformance kit: a variant with packages and cached images checks that every task the runner writes installs first, every image is built, and the registry's vars reach no step that runs the repository's code

### Deprecated

- Config: `suites.<name>.lock` is no longer read, and `check` names each one still set; its `minutes` is still read as the old spelling of `suites.<name>.minutes`. Remove it once the pipeline runs this release: a pipeline still running an older pipelinegen fails its lock steps without it. A later release refuses it
- Commands: `pipelinegen lock` and `pipelinegen unlock` do nothing and exit 0, whatever their arguments, so lock steps that run this release pass until the pipeline is set again; `lock acquire --started <dir>` still stamps the directory its batch end reads. The next release removes both

### Removed

- Locks between runs. A suite's batches no longer take a lease: the wait for memory (above) replaces it, since the room a batch waits for is the worker's, whichever repository's pipeline holds the rest, and placement can still put two pipelines' heavy suites on different workers. The spec carries no `lock`, `lock_minutes` or `lock_wait`, the runner protocol drops the `lock_per_batch` capability, and `check` no longer warns of batches queued on a lock. A runner given an older tool's spec with a lock writes no lock step and says so in a notice; the conformance kit refuses a pipeline in which a step runs `pipelinegen lock` or `unlock`. The trusted keys hold no locks, and an older pipeline's that do are no edit
- The leases older pipelines left are swept: a trusted run's one-step plan deletes the refs under `refs/pipelinegen/locks/`, or the files under `locks/` on the branch store's `-locks` branch, and only warns when it can't. Leases kept in another repository (`lock.repository`) stay; nothing reads them

### Fixed

- `check` fetches a plugin it asks that `--plugins` (default `tools/plugins`) lacks from its pinned release, as `affected` and `run` do, checked by its pin, where it warned that the driver's steps went unchecked. A plugin pinned by `commit` that isn't there still can't be asked: the warning gives the commands that build it
- `why` plans on when the remote's lists can't be read, as `affected` and `run` do: the recorded slices are bound to everything, and a warning below the reasons says the lists couldn't be read and why. It exited 1, even for a config whose every suite is bound by the graph
- `run` without `--work` says why a slice failed in full on stderr. It named the slice's ref in the run's own store, which was removed when the run ended

## [0.3.0] - 2026-11-17

### Added

- Config: `plugins[].kind: connector`, named `github` or `git`, pins another release of that connector (`release` with `sha256` or `sums`; no `config`)
- `run` and `record` run a suite's steps in the image CI runs them in: the suite's `image` (or its prepare step's), a `registry-image` resource of `concourse.include`, pulled by its pinned digest when it has one, or else the task file's `image_resource`, with `docker` or `podman` (`--container-tool`). The build's directory, the repository's git directory (read only) and the tools are mounted at their own paths, the environment is the task's own (`PIPELINEGEN_*` and `params`, never a token), the suite's `privileged` holds, and the network is the default bridge. The steps run as root with the image's `HOME`, as on CI, and what they wrote is handed back to you when each ends, failed or stopped; `--as-me` runs them as you. In an image that isn't this machine's platform, or when this pipelinegen isn't a static Linux build, the release `concourse.planner.bootstrap.release` pins runs there. `--no-image` runs on this machine as before; with no container tool, a suite that names an image fails and says so
- `affected`, `why`, `try`, `run`, `record` and `slice run` fetch a plugin `--plugins` (default `tools/plugins`) lacks from its pinned release on GitHub, for this machine's platform or the image's, into the user cache, checked by its `sha256` or by the release's `SHA256SUMS` (`sums`), so a fresh clone needs nothing in `tools/plugins`. A driver there that isn't its pin is passed over for the pinned release. No token is needed, and a release no command used in 30 days is removed from the cache. A plugin pinned by `commit` can't be downloaded: the error names it and how to build it
- Conformance kit: a connector fixture may say why its host has no forks (`"no_forks"`), and then checks that the changes it names in `same` run
- `compare: last_passed`: while the new core narrowing `passed-since` is on (levels 1 to 3), each slice is compared from the newer of the run's base and the newest earlier commit at which it passed in the same run with its tests run, read from the run's records within `keep.runs`. For the newest two such commits the plan finds what changed from there to the head before the sample is drawn, and leaves out a slice that passed at one of them when nothing since reaches it, under `narrowed` with the reason "compared from `<commit>`, where it last passed in run `<run>`", which `pipelinegen why` shows. A run kept red by one slice re-ran every slice the changes since its last whole pass reached, on every commit. A slice with no such pass, one that ran no tests or was not run, the sample's targets, a suite whose whole-suite checks run, and a commit after which the config changed keep the run's base
- Narrowing: `build-files-by-targets` (the core's, off from 5). A `BUILD`, `BUILD.bazel` or `.bzl` file in a recorded slice's filegroups is no data file of the slice: a changed rule reaches it through that rule, which bazel-diff hashes, and a changed member list as files added or removed. Adding a unit test to an app's test lists ran every spec whose data holds the app's shell, since the shell listed its BUILD file; it now runs the specs with no list yet, and the plan names the rest under the narrowing
- Config: `narrowings` sets the level one narrowing is off from, in place of the level its plugin or the core declares, with a required note: `narrowings: {document-units: {off_at: 3, note: "every contract's readers and per_operation declared"}}`. `off_at` is 1 to 5 and may raise or lower the declared level; a name no configured plugin declares, and that isn't one of the core's, is refused when planning. Every plan records the levels the config set, and the plan's narrowed lines, `why` for a slice a narrowing left out and the nightly's alarm say so (`off from 3 by the config (narrowings.document-units: ...); plugin openapi-sbom says 2`); the alarm also names the `narrowings` entry to lower. A change to `narrowings` counts as a change to what is selected: the change plans under both configs and runs what either selects

### Changed

- Connectors are plugins in repositories of their own: GitHub's statuses, comment and pull requests go through the GitHub connector ([pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github)), and a plain git server's review branches through the git connector ([pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git)). pipelinegen pins a release of each and fetches the one a config uses, checked by its checksum, the way it fetches bazel-diff, so a config with a `github` section or a `git` connector needs no new line. A generated pipeline's bootstrap fetches it with the other plugins, with the read token; anywhere else pipelinegen downloads it from the connector's GitHub release into its cache the first time a command talks to the host, which needs github.com and a public repository. Regenerate the pipeline after upgrading. The connector holds no token: pipelinegen sends its requests, with the token of each call's purpose, to the step's API host only
- Pull request admission asks the connector's listing of open pull requests, not the one pull request: a pull request retargeted to another base reads "closed, or no longer onto `<base>`", and a pull request from a fork, or from a head repository GitHub no longer has, is never planned, by the head repository's id rather than its name
- `once_per_ready` finds an earlier head of the pull request in the run's own records, a plan written since it was last marked ready, where it read that head's commit statuses. Records older than `keep.runs` are gone, so a pull request ready for longer than that runs again after a push
- The first step of a two-step plan (`plan --out`) that holds the read token alone plans without asking whether the run takes the pull request, a draft or a closed one too, and runs its code there as before: no connector call carries the read token. The second step asks with the changes token, as before, and writes no plan for a pull request the run leaves out
- The sweep's give-up status goes through the connector's `status` call, with the report token, or the changes token when the sweep holds no report token
- A pull request's comment: the comment kept is the oldest that starts with the run's marker, and it is updated only outside the report throttle of the run's last status, where it waited for its own last update. A run that reports no statuses updates its comment whenever its text changes
- A call to GitHub without its purpose's token in the step's environment fails, naming the purpose, where it was sent without a token
- Planning opens a recorded slice's filegroups (and aliases, and the filegroups inside them) down to their files, with one Bazel query per level, cached by commit under `groups/` in the cache. A file an sbom lists units for is then replaced by the units the slice touched inside a filegroup too: with an anchor where the slice holds it, without one where the suite's recordings hold units of that sbom for files of the same directory. Editing one migration ran every spec whose stack holds the migrations; it now runs the specs that touched its tables. A file with units added or removed in such a group reaches the suite's setup part where the setup list holds its sbom's units and the setup split is on, and stays whole for every slice otherwise; a rename keeps it whole. Other files, other rules, a filegroup with a `select()` or an `output_group`, generated files and other repositories' labels stay whole, and a filegroup in which a slice gives up nothing stays one label. A reason names the filegroups its file was found through
- Planning and binding judge a data file alike: binding now keeps a file a test reads as text (`text_inputs`) or names in its `pipelinegen:data` lines, and a file whose anchor the slice's list lacks, as the walk did
- Continuous runs: the planning job is never serial. Its builds run side by side, and each plan step records its round's start and planning in the run's records, reads the others', exits when its commit is no longer the branch's tip, and waits, at most `runs.<run>.wait_minutes`, while an older round is under way. The spec no longer sets `serial_plan`, and `PIPELINEGEN_SERIAL_PLAN` is no longer read: a pipeline set by an earlier runner, whose planning job is serial, records its rounds too, every plan of a continuous run now has `limits.plan` as its deadline, and a build of a commit whose round a build that died had started runs nothing until `limits.plan` and 10 minutes have passed (or `pipelinegen end` ends that round). The runner protocol drops the `serial_jobs` capability; a runner that still declares it is asked for nothing serial, and the conformance kit refuses a serial planning job and a step that sets `PIPELINEGEN_SERIAL_PLAN`
- Batches: every batch of a suite's build runs at once. `suites.<name>.parallel` (4 by default) is how many batches a build is dealt, and so how many run together; `batches` and `batch_minutes` deal more, as before, and all of those run at once too. `check` warns of a suite whose `batches` is more than four times its `parallel`, of one with `batch_minutes` and no `batches`, whose run of everything has no bound on its batches, and of one whose batches, more than `parallel`, take a lock one by one and whose last could wait past the lock's 4 hours, since every batch asks for its slot at once

### Deprecated

- Config: `suites.<name>.run_on.serial_group`, `suites.<name>.run_on.max_in_flight` and `concourse.serial_groups` are no longer read: no job is serial, in a serial group or capped, and `check` names each one still set; a later release refuses them

## [0.2.4] - 2026-11-17

### Fixed

- Pull request runs: when the target branch moves on between a plan's two steps, the second step takes the first step's plan of the change merged into the older tip, once it finds that tip in the target's history and gets the same merge itself; it refused the plan as "not of this change" and failed the run

## [0.2.3] - 2026-11-17

### Added

- `pipelinegen run` plans a change as `affected` does and runs the slices it selects on this machine, as a pull request's run would: per suite, its prepare once and one batch of the selected slices, through its drivers or task files, then a summary of the slices that passed, failed and errored, exiting 1 when any did not pass. `--base` is required unless `--all`; uncommitted changes are neither planned nor run, and a line names them. `--record` records the suites bound by recording and attaches their lists to the head in the local store when every slice of the suite passed. It needs no token, host or `runs` section

### Changed

- Config: `concourse.planner.bootstrap.bazelisk` is optional; without it the pipeline fetches bazelisk v1.30.0, checked against its pinned checksum

### Fixed

- Pull request runs: a change that plans every slice for a reason known before any tree is read (a changed `everything_on` file, among others) now records its base in the plan. Planning in two steps refused such a plan as "not of this change", and its suites could not merge the change

## [0.2.2] - 2026-11-17

### Fixed

- Pull request runs: a planning build that ends without a plan (the run left the head out, as a draft, or its plan failed) removes the record that its planning began, in its write step (`plan --from`), so the run's sweep takes the head up after `missed_after`. The sweep left such a head to the ended build until `limits.plan` and ten minutes had passed, two hours and ten minutes by default

## [0.2.1] - 2026-11-16

### Added

- Config: `plugins[].collect_seconds` bounds a correlator's collect calls (10 by default); in a batch run by drivers a call also stops 30 seconds before the batch's time limit, and `check` refuses a recorded suite whose correlators may take more than 90 seconds together. A call stopped by its limit says so, with the call and the slice (`sql collect (slice-end of <slice>): stopped after 10s, the limit for a collect call`), where it said `signal: killed`

### Fixed

- Recording: a batch run by drivers observes its slices in its own task, where its tests ran and its correlators collected, after its last slice, and keeps each correlator's answers for the batch's end, which observes only what the batch left (a correlator that failed there, or one the batch had no time for). The batch's end runs in the pipeline's planner image, which may lack what a correlator needs (node for react-correlator), and every slice ended errored there with "exit status 127" after its tests had passed. An observation in the batch never changes a slice's result and stops two minutes before the task's limit. Before its first slice a recording batch checks the interpreter each correlator's file names on its first line, and stops at once, naming the suite, the correlator, the interpreter and the task, when the task lacks it; the batch's end says the same once, and fails only the slices it still had to observe

## [0.2.0] - 2026-11-16

The first release.

- `slices`, `check`, `affected`, `why` and `record` work on a developer's
  machine with no CI system; `seed` puts local recordings into the
  repository's store.
- Planning walks a stored graph outward from what changed, for every suite,
  and joins that with what bazel-diff says changed.
- Plugins: sboms and correlators say what the code holds and what each test
  executed; drivers run a suite's slices; runners write the pipeline;
  connectors say where the code lives (GitHub and plain git built in).
- Runs (`main`, `nightly`, continuous branch runs, pull requests) are
  declared by name in `pipelinegen.yaml`; a change to the config is judged
  by what it means for the tests.
- A run keeps its records in a few refs of the repository, and lists are
  kept by commit.
- Tokens and ssh keys are split by purpose, and a step gets only the ones it
  needs; a pull request's tasks get none that can write.
- The plugin conformance kit (`pipelinegen plugin test`) checks a plugin
  against the protocol.
- Releases carry static binaries for Linux and macOS, with `SHA256SUMS`.
