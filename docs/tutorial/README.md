# Step by step: pipelinegen on your project

This part takes your repository from nothing to a CI pipeline that runs
only the tests a change reaches. Each step says what to do, what you
should see afterwards, and what to do if you see something else.

The examples come from a Go service like the [running
example](../concepts/README.md#the-running-example): a Go service with
five Go test files and a small web page with two browser specs. Your
repository will have other names and more files, but the steps are the
same.

## Before you start

Decide three things, because some steps differ by them:

| Question | Your answer decides |
|---|---|
| Does the repository already build with Bazel? | Whether you do [step 1](bazel.md) in full or only check it. |
| Which languages are the tests in? | Which plugins you add in [step 6](plugins.md), and which drivers run the tests in [step 7](tasks.md). Go is covered end to end; for TypeScript and browser tests the step points you to the plugin's own guide. A language without plugins uses the build graph alone and skips step 6. |
| Which CI system and git host? | Steps 1 to 8 need neither. Steps 9 to 12 use Concourse, the CI system with a runner plugin today, and GitHub, the host that shows statuses and a comment; [what works today](../introduction/what-works-today.md) says what else works. |

You need a Linux or macOS machine with git and Go 1.27 or newer. The steps
take an afternoon for a small repository; most of the time goes to the
first Bazel build and to the first CI run.

## The steps

1. [Bazel for your project](bazel.md): add a Bazel workspace, or check the
   one you have.
2. [Install pipelinegen](install.md).
3. [A first config](first-config.md): one suite, and the list of slices.
4. [The check](check.md): make every file covered or explained.
5. [The first plan](first-plan.md): see what a change would run, and why.
6. [Plugins for your language](plugins.md): bind tests to the code they
   run.
7. [The suites' steps](tasks.md): the drivers that run each suite's
   tests, and the server the browser specs talk to.
8. [See it on your machine](on-your-machine.md): record once, then watch a
   backend change select the one browser test it reaches.
9. [The pipeline](pipeline.md): generate it and set it on Concourse.
10. [The first run](first-run.md): everything runs once and is recorded.
11. [Pull requests](pull-requests.md): the comment, the statuses, the
    branch rule.
12. [The nightly](nightly.md): the run that checks everything.
13. [Upgrading](upgrading.md): new releases of pipelinegen and its
    plugins.

When something goes wrong at any step,
[troubleshooting](../troubleshooting.md) lists the errors you can meet, by
the message pipelinegen prints.
