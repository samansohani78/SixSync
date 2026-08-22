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

## M2 — Planning · `v0.2.0`

**Goal:** produce a reviewable artifact describing what would change, without
changing anything.

- `plan`, writing a deterministic JSON artifact
- Dependency graph across `upstream_id`, `service_id`, `plugin_config_id`
- Blast-radius reporting: how many routes a shared plugin config change affects
- Route collision detection
- `--detailed-exitcode` on `plan`

**Done when:**

- the same inputs and remote state produce byte-identical plan JSON, proven by
  a golden test
- a plan records `modifiedIndex` and a canonical digest per affected resource
- changing a plugin config referenced by 20 routes reports all 20
- two routes with identical match criteria are reported before apply
- a plan containing a deletion is visibly marked as such

## M3 — Apply · `v0.3.0`

**Goal:** the first write path. The milestone where being wrong causes an
outage, so the bar is correspondingly higher.

- `apply`, consuming a plan artifact rather than recomputing
- `modifiedIndex` revalidation immediately before each write, aborting on change
- PUT to stable identifiers only; never PATCH, never POST
- Explicit deletion honouring `lifecycle.state: absent`, with dual confirmation
- Pre-change journal
- `rollback` from a journal, best-effort and honest about it

**Done when:**

- applying an unchanged repository issues **zero** write requests, verified by
  counting them — a redundant write advances `modifiedIndex` and is real churn
- a plan built against state that has since moved is refused with exit code 3
- an injected mid-apply failure restores prior state in an integration test, and
  reports exit code 5 when it cannot
- deletion without `lifecycle.state: absent` is impossible, asserted by test
- `?force=true` is never sent
- applying to a viewer key fails before the first write, not partway through

## M4 — Compatibility and hardening · `v0.4.0`

- APISIX 3.16 and 3.17 in the integration matrix
- Consumer support, only if credentials can be handled without writing plaintext
  to Git — likely via `$secret://` and `$env://` references
- Fuzz tests for parsing, canonicalization and redaction
- Behaviour at 5,000 resources
- Signed releases, checksums, SBOM

**Done when:**

- the integration suite passes on 3.16.0, 3.17.0 and 3.18.0, with any divergence
  documented in `compatibility.md`
- `plan` over 5,000 resources completes within a stated budget on stated hardware
- no high-severity `govulncheck` finding at release
- either consumers are supported with no plaintext credential in Git, or the
  refusal is documented as permanent

## M5 — v1.0

- Stable resource format, exit codes, plan and journal schemas
- A documented migration path from `v1alpha1`
- External security review
- At least one production adopter other than the author

**Done when:** each of the above is true. The last item is not a formality — a
tool this consequential should not declare 1.0 on the author's own use alone.

## Explicitly not planned

- Writing to etcd — [ADR-0001](docs/decisions/ADR-0001-admin-api-only.md)
- Delete-by-omission as the default — [ADR-0002](docs/decisions/ADR-0002-explicit-reviewed-deletion.md)
- Replacing APISIX Ingress Controller
- A web UI or hosted control plane
- Telemetry of any kind
- Managing APISIX installation, etcd, or host configuration
- Templating. Render with Helm, Kustomize or ytt before invoking SixSync; the
  format stays deterministic.
