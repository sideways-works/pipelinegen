# Contents

* [The pipelinegen book](README.md)

## Introduction

* [What pipelinegen is](introduction/what-it-is.md)
* [Is it for you?](introduction/is-it-for-you.md)
* [What works today](introduction/what-works-today.md)

## Concepts

* [Concepts and the running example](concepts/README.md)
  * [Changes and tests](concepts/changes-and-tests.md)
  * [What Bazel does](concepts/bazel.md)
  * [Suites, slices and batches](concepts/suites-and-slices.md)
  * [Drivers: how a batch runs](concepts/drivers.md)
  * [Units and sbom plugins](concepts/units-and-sbom.md)
  * [Links and correlators](concepts/links-and-correlators.md)
  * [Recordings and lists](concepts/recordings-and-lists.md)
  * [The graph and the walk](concepts/the-graph-and-the-walk.md)
  * [Narrowing](concepts/narrowing.md)
  * [Bazel and the plugins side by side](concepts/bazel-and-plugins.md)
  * [Runs and the pipeline](concepts/runs-and-the-pipeline.md)
  * [Connectors](concepts/connectors.md)
  * [The store](concepts/the-store.md)
  * [Refreshing the lists](concepts/refreshing-the-lists.md)
  * [The nightly](concepts/the-nightly.md)
* [Glossary](glossary.md)

## Step by step

* [Pipelinegen on your project](tutorial/README.md)
  * [1. Bazel for your project](tutorial/bazel.md)
  * [2. Install pipelinegen](tutorial/install.md)
  * [3. A first config](tutorial/first-config.md)
  * [4. The check](tutorial/check.md)
  * [5. The first plan](tutorial/first-plan.md)
  * [6. Plugins for your language](tutorial/plugins.md)
  * [7. The suites' steps](tutorial/tasks.md)
  * [8. See it on your machine](tutorial/on-your-machine.md)
  * [9. The pipeline](tutorial/pipeline.md)
  * [10. The first run](tutorial/first-run.md)
  * [11. Pull requests](tutorial/pull-requests.md)
  * [12. The nightly](tutorial/nightly.md)
  * [13. Upgrading](tutorial/upgrading.md)
* [Worked example: a real monorepo](worked-example/README.md)

## How-to guides

* [How-to guides](how-to/README.md)
  * [Add a test](how-to/adding-a-test.md)
  * [Add a suite](how-to/adding-a-suite.md)
  * [Write a suite's tasks](how-to/writing-tasks.md)
  * [Bundle a suite's slices, or run each alone](how-to/batching.md)
  * [Move a suite from task files to drivers](how-to/moving-to-drivers.md)
  * [Configure plugins](how-to/plugins.md)
  * [Estimate what you would save](how-to/estimate-savings.md)
  * [Declare runs](how-to/declaring-runs.md)
  * [Start without a full first run](how-to/starting-narrow.md)
  * [Run it on your machine, without CI](how-to/running-locally.md)
  * [Run it on any CI system by hand](how-to/any-ci-by-hand.md)
  * [Choose a narrowing level](how-to/narrowing.md)
  * [Durations and batches](how-to/durations.md)
  * [Share machines](how-to/sharing-machines.md)
  * [Secrets](how-to/secrets.md)
  * [Packages and cached images](how-to/packages-and-images.md)
  * [Uncovered files at planning](how-to/uncovered.md)
  * [Look inside: lists and why](how-to/looking-inside.md)
  * [Attach lists after a merge](how-to/attaching-lists.md)
  * [Keep history](how-to/keeping-history.md)
  * [Back up the store](how-to/backups.md)
  * [Fresh clones and old setup lines](how-to/fresh-clones.md)
* [Troubleshooting](troubleshooting.md)

## Reference

* [Config](reference/config.md)
* [Commands](reference/commands.md)
* [The check](reference/check.md)
* [Where pipelinegen keeps things on your machine](reference/your-machine.md)
* [The plugin protocol](plugins.md)

## Writing plugins

* [Writing plugins](writing-plugins/README.md)
  * [An sbom plugin](writing-plugins/sbom.md)
  * [A correlator](writing-plugins/correlator.md)
  * [A runner](writing-plugins/runner.md)
  * [A driver](writing-plugins/driver.md)
  * [A connector](writing-plugins/connector.md)
  * [A store (a design)](writing-plugins/store.md)

## For contributors

* [Contributing](../CONTRIBUTING.md)
* [Internals](internals/README.md)
  * [Planning](internals/planning.md)
  * [Binding by recording](internals/binding.md)
  * [The stored graph and the walk](internals/graph.md)
  * [The generated pipeline](internals/pipeline.md)
  * [Refs](internals/refs.md)
  * [The lock, removed](internals/lock.md)
  * [The reporter](internals/reporter.md)
  * [Batches](internals/batches.md)
  * [Safety](internals/safety.md)
  * [Concourse](internals/concourse.md)
  * [Testing and releases](internals/testing-and-releases.md)
* [Design](design.md)
* [Contracts](contracts.md)
* [Proof](proof.md)
* [Releases](releases.md)
* [Changelog](../CHANGELOG.md)
