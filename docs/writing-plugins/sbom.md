# Writing an sbom plugin

This page is for people writing an sbom plugin. An sbom plugin says what
exists in one domain: Go declarations and modules, TypeScript functions
and components, npm packages, database tables and views, API operations.
Each thing is a unit with an ID and a digest, and the plugin also says how
its own units hang together. The fields are in
[plugins.md](../plugins.md#sbom-plugins).

## Calls

- `describe`: the plugin's kind, version, namespaces, `candidates` (cheap
  path patterns such as `**/go.mod`), and per call whether its answer
  depends on one file (`per-file`) or on a set of files (`per-set`).
- `detect`: claim the candidate files you have positive evidence for, each
  with a role. A YAML file is an API contract because it has an `openapi`
  key, not because of its name.
- `units`: the units of the claimed files, the links among your own units
  (a view reads its tables, a module requires another), and the units you
  couldn't read, under `cannot_tell`.

The protocol also defines `document`, a standard bill of materials
(CycloneDX) for an sbom of dependencies. The core doesn't call it yet.

## IDs and digests

A unit's ID lives in your namespace, named for what owns its meaning:
`go:`, `ts:`, `dep:npm:`, `pg:`, `api:`. Correlators link into it by these
IDs, so put the code that spells them in a small library your correlators
share, and keep an ID stable across versions: a dependency bump should move
only its digest.

A digest changes exactly when the unit's behaviour can change. For code,
hash the syntax tree without comments or positions; a comment edit that
changes a digest makes every comment select tests. A unit may take its
digest from other units instead (`digest_from`), which is how an sbom
offers a name-only alias over several units.

## Files in, JSON out

Read the files and nothing else: a schema from its migration files by
parsing them, a dependency's version from the lockfile. Start no database
and use no network. A per-file answer must not depend on any other file,
since the core caches it by that file's blob hash; a per-set answer names
the smallest set it depends on. Keep no cache of your own for what the
core caches.

The answer may depend only on the files you claimed and those you read
along with a claim. The kit checks it: it gives every other file of your
fixture other bytes and fails the plugin when its units change.
