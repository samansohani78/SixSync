# Versioning and migration

## Releases

Semantic versioning. While the major version is `0`, the public contracts below
may change between minor releases, always with migration notes in the changelog
and, where a file format is involved, a version field that makes the old form
detectable rather than silently misread.

## What counts as a public contract

Changing any of these is a breaking change:

- **Exit codes.** Pipelines branch on them. Codes reserved for commands that do
  not exist yet are declared in advance so a future command cannot claim a
  number scripts already read as meaning something else.
- **The resource file format**, identified by `apiVersion: sixsync/v1alpha1`.
- **The plan artifact**, identified by `format_version: sixsync.plan/v1alpha1`.
- **The execution journal**, identified by
  `format_version: sixsync.journal/v1alpha1`.
- **The policy profile**, identified by `apiVersion: sixsync/v1alpha1`.
- **Lint rule identifiers**, which appear in output and in review comments.
- **Flag names and their meanings.**

Not contracts: human-readable output wording, log formatting, and anything
under `internal/`.

## How a format change works

Each format carries its own version, and each is read strictly — unknown fields
are rejected rather than ignored. That is deliberate: a field this build does
not understand means the artifact came from a different version, and acting on
something not fully understood is how a tool with write access causes an
incident.

When a format changes:

1. The version string changes.
2. The release notes say what changed and what to do.
3. Where a mechanical migration is possible, a command is provided.
4. The previous version is read for at least one minor release, with a warning.

An artifact from a newer SixSync than the one reading it is always refused. A
plan is executed by `apply`, so guessing at an unfamiliar field there could
mean applying something other than what was reviewed.

## APISIX versions

Support is claimed only for versions the integration suite passes against, and
the matrix in [compatibility.md](compatibility.md) records the result along
with the differences between them. Dropping a version is a breaking change and
gets a minor version bump with notes.

Adding one requires the suite to pass unmodified, or a version-aware
expectation that states the difference explicitly. Differences are never
smoothed over with a generic normalization, because that hides the very thing
an operator needs to know.

## Go version

`go.mod` declares the minimum Go version. SixSync builds on the two most recent
major Go releases, which is what upstream supports. Raising the minimum is a
minor version bump.

## Deprecation

A deprecated flag or field keeps working for at least one minor release and
warns on use. The warning names what to use instead.
