# Step 4: the check

Running only the tests a change reaches is safe only if every file of the
repository is something a test depends on. A file no test depends on is a
blind spot: a change to it would run nothing. `pipelinegen check` finds
those files, and you decide for each whether a test should cover it or why
none can.

## Run it

On the notes service, right after the first config:

<!-- output: check-first -->
```
$ pipelinegen check
.bazelversion: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
.gitignore: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
BUILD.bazel: no slice depends on this file, so a change to it would run nothing
  fix: make a slice depend on a target of this package, or add the file to untested in pipelinegen.yaml with a reason
MODULE.bazel: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
MODULE.bazel.lock: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
cmd/notes/BUILD.bazel: no slice depends on this file, so a change to it would run nothing
  fix: make a slice depend on a target of this package, or add the file to untested in pipelinegen.yaml with a reason
cmd/notes/main.go: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
go.mod: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
go.sum: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
package-lock.json: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
package.json: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
pipelinegen check: 11 findings: did not pass
[exit 1]
```

The findings fall into three groups, and each has its own answer.

The build setup (`MODULE.bazel`, its lock file, `.bazelversion`, `go.mod`
and `go.sum`, `package.json` and its lock file, and the CI files under
`ci/` that [step 9](pipeline.md) adds) can change how every test builds or
runs. List it under `everything_on`: a
change to it then runs every slice, and the check counts it as covered.

The root `BUILD.bazel` holds only Gazelle's own target, and `.gitignore`
affects no test. They go under `untested`, each group with its reason.

Code no test is built from (`cmd/notes/main.go`, which starts the server)
goes under `untested` too, with the reason. Here the browser specs do run
it, through the server, but the check reads the build graph and not
recordings. If no test runs such code, write one; the check is how you
notice.

Add this to `pipelinegen.yaml`:

```yaml
# A change to the build setup runs every test.
everything_on: [MODULE.bazel, MODULE.bazel.lock, .bazelversion, go.mod, go.sum, package.json, package-lock.json, "ci/**"]

untested:
  - paths: [BUILD.bazel]
    reason: holds only Gazelle's own target
  - paths: [.gitignore]
    reason: repository settings
  - paths: ["cmd/notes/**"]
    reason: starts the server; the browser specs run it, bound by their recordings, which the check doesn't read
```

<!-- output: check-passes -->
```
$ pipelinegen check
```

**You should see** no output, and exit status 0 (`echo $?`).

**If you don't**, each finding names its file and a fix. The full list of
findings is in [the check's reference](../reference/check.md).

## A large repository

On a repository with hundreds of files the first check prints hundreds of
findings. `--summary` counts them by kind and top-level directory instead:

<!-- output: check-summary -->
```
$ pipelinegen check --summary
9	uncovered-file	.
2	uncovered-file	cmd/
11 findings
pipelinegen check: 11 findings: did not pass
[exit 1]
```

Then decide per directory: documentation and prose (`docs/**`, `**/*.md`)
go under `untested`; a directory Bazel ignores (examples, fixtures that
`.bazelignore` lists) shows as one finding, since nothing in it can be
covered, and goes under `untested` whole; source that no test covers is
worth a second look.

## As a commit hook

Run the check before every commit, so new files are decided on when they
are added:

```sh
# .git/hooks/pre-commit
pipelinegen check --staged || exit 1
```

`--staged` looks only at what the commit touches. The planning job runs
the same check on the whole tree: a file the hook let through makes that
run select every slice, or fails it if the config says `uncovered: fail`.

Commit the config, then go on.

Next: [step 5, the first plan](first-plan.md).
