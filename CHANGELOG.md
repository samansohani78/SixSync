# Changelog

Notable changes, newest first. Release notes are generated from commit
messages by GoReleaser; this file records the shape of each release and
anything that needs a person's attention when upgrading.

Format loosely follows [Keep a Changelog](https://keepachangelog.com/).
Versioning is documented in [docs/versioning.md](docs/versioning.md).

## Unreleased

First working version. Nothing has been released yet, so there is no upgrade
path to describe.

### Added

- `version`, `doctor`, `import`, `lint`, `validate`, `drift` — the read-only
  workflow. None of these change remote state.
- `plan` — a deterministic, target-bound artifact describing what would
  change. The same repository against the same gateway state produces
  byte-identical content and the same digest.
- `apply` — executes a plan behind explicit approval, re-reading every
  affected resource first and abandoning the run if anything moved.
- `rollback` — restores what an apply changed, from its journal.
- Configurable policy profiles with limits, protected resources, blast-radius
  caps and required labels.
- Eleven lint rules, including route, filename and case collisions.
- Reproducible releases for Linux, macOS and Windows on amd64 and arm64, with
  checksums, SPDX SBOMs and keyless cosign signatures.

### Compatibility

Apache APISIX 3.16.0, 3.17.0 and 3.18.0, each verified by the integration
suite. Two behavioural differences on 3.16.0 are documented in
[docs/compatibility.md](docs/compatibility.md).

### Not supported

Consumers, Credentials, SSL, Secrets, StreamRoutes, ConsumerGroups, Plugin
Metadata and Protos are recognized and refused by name with a reason. For the
credential-bearing kinds this is deliberate and unlikely to change: APISIX
returns decrypted secrets on read, so importing them would write live
credentials into Git.
