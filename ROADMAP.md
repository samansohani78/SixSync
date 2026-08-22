# Roadmap

Every milestone states what must be demonstrably true to call it done. "Support
production" is not a milestone; "an import round-trip reports no drift on a
seeded APISIX 3.18.0 instance" is.

## M1 — Read-only foundation · **complete**

- `version`, `doctor`, `import`, `lint`, `validate`, `drift`
- Admin API client, kind registry, resource format, canonicalization, redaction
- Pinned Compose environment and an integration suite

Demonstrated:

- `import` then `drift` reports no differences against a seeded APISIX 3.18.0
- two imports into separate directories are byte-identical
- `doctor` distinguishes viewer from administrator keys without mutating
- reordered sets and both node encodings do not register as drift
- no command changes remote state; asserted by test

Not done: mutation of any kind.

## M2 — Planning · **complete**

**Goal:** produce a reviewable artifact describing what would change, without
changing anything.

- `plan`, writing a deterministic JSON artifact
- Dependency graph across `upstream_id`, `service_id`, `plugin_config_id`
- Blast-radius reporting: how many routes a shared plugin config change affects
- Route collision detection
- `--detailed-exitcode` on `plan`

**Demonstrated:**

- the same inputs and state produce a byte-identical artifact and digest,
  across repeated runs and at 1,000 resources
- runtime metadata is outside the hashed content, so it cannot disturb it
- a plan records `modifiedIndex` and a canonical digest per affected resource
- a shared plugin config change reports every route it reaches, directly and
  transitively
- blocked operations — dangling references, unsupported kinds, uncomparable
  shapes — make a plan visibly unapplicable rather than being omitted

Not done: route collision detection, which is deferred to M5.

## M3 — Apply · **complete**

**Goal:** the first write path. The milestone where being wrong causes an
outage, so the bar is correspondingly higher.

- `apply`, consuming a plan artifact rather than recomputing
- `modifiedIndex` revalidation immediately before each write, aborting on change
- PUT to stable identifiers only; never PATCH, never POST
- Explicit deletion honouring `lifecycle.state: absent`, with dual confirmation
- Pre-change journal
- `rollback` from a journal, best-effort and honest about it

**Demonstrated:**

- applying an unchanged repository issues zero writes, asserted against both a
  fake and a real gateway by comparing `modifiedIndex` before and after
- a plan built against state that has since moved is refused with exit code 3,
  verified against a live APISIX with a genuine concurrent write
- failure injected at every operation boundary leaves rollback able to remove
  what was created
- deletion requires a tombstone *and* `--approve-delete`
- `?force=true` is never sent
- a tampered plan, a plan for another gateway, and a plan built with another
  credential are all refused before the first write

## M4 — Compatibility and hardening · **complete**

- APISIX 3.16 and 3.17 in the integration matrix
- Consumer support, only if credentials can be handled without writing plaintext
  to Git — likely via `$secret://` and `$env://` references
- Fuzz tests for parsing, canonicalization and redaction
- Behaviour at 5,000 resources
- Signed releases, checksums, SBOM

**Demonstrated:**

- the integration suite passes on 3.16.0, 3.17.0 and 3.18.0 — 38 tests each —
  with both divergences documented and encoded as version-aware expectations
- `plan` over 5,000 resources completes in 138 ms with a 17 MiB peak heap
- fuzz targets cover every parser and canonicalizer; two real redaction
  bypasses and a path-traversal-shaped identifier were found and fixed
- `govulncheck` reports nothing
- releases are reproducible, checksummed, SBOM'd and keyless-signed

Consumers remain unsupported. See the non-goals below.

## M5 — v1.0 · in progress

Remaining: route collision detection, a documented migration path exercised in
practice, external security review, and a production adopter other than the
author.



- Stable resource format, exit codes, plan and journal schemas
- A documented migration path from `v1alpha1`
- External security review
- At least one production adopter other than the author

**Done when:** each of the above is true. The last item is not a formality — a
tool this consequential should not declare 1.0 on the author's own use alone.

## Deliberate non-goals

**Consumers, Credentials, SSL, Secrets.** APISIX enables `data_encryption` by
default and decrypts `encrypt_fields` when serving a read, so
`GET /apisix/admin/consumers/{name}` returns plaintext credentials. Importing
them would write live secrets into Git. SSL objects cannot round-trip at all,
because APISIX strips the private key on read — and on 3.16.0 it returns that
key encrypted under a keyring whose default values are published.

These kinds are recognized and refused by name with a reason, never silently
skipped, and the refusal has test coverage. Revisiting this requires a design
where the secret never enters the repository — most plausibly by managing only
`$secret://` and `$env://` references.

## Explicitly not planned

- Writing to etcd — [ADR-0001](docs/decisions/ADR-0001-admin-api-only.md)
- Delete-by-omission as the default — [ADR-0002](docs/decisions/ADR-0002-explicit-reviewed-deletion.md)
- Replacing APISIX Ingress Controller
- A web UI or hosted control plane
- Telemetry of any kind
- Managing APISIX installation, etcd, or host configuration
- Templating. Render with Helm, Kustomize or ytt before invoking SixSync; the
  format stays deterministic.
