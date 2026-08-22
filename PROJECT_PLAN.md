# SixSync — project plan

Supersedes `APISIX_GITOPS_PROJECT_PLAN.md`, which is kept unchanged as the
original brief. See [Changes from the original plan](#changes-from-the-original-plan).

## Problem

Apache APISIX has a capable Admin API, but operating hundreds of routes through
a dashboard or ad-hoc `curl` leaves the live gateway as the only record of what
is deployed. Changes go unreviewed, drift accumulates, and a mistake in a shared
plugin config reaches every route referencing it at once.

Existing declarative tooling solves part of this. It does not solve it in a way
that suits adopting a large, already-running gateway — see
[Comparison with ADC](#comparison-with-adc).

## Target users

SRE and platform teams running APISIX on VMs or bare metal, in traditional
etcd-backed mode, who want gateway changes to go through review.

Kubernetes users whose workflow is already covered by APISIX Ingress Controller
are not the audience.

## Non-goals

- Writing to etcd — [ADR-0001](docs/decisions/ADR-0001-admin-api-only.md)
- Replacing APISIX Ingress Controller
- Templating (pre-render with Helm, Kustomize, ytt)
- A web UI, a hosted control plane, telemetry of any kind
- Managing APISIX installation, etcd, or host configuration
- Claiming atomicity across resources that APISIX itself cannot provide

## Threat model

SixSync holds a credential equivalent to control of the gateway. See
[docs/security-model.md](docs/security-model.md).

In scope: credential disclosure through any output path; secret material
reaching a resource file; TLS weaker than documented; a read-only build changing
state; deletion of anything not explicitly marked `absent`.

Out of scope: APISIX's own vulnerabilities; consequences of an operator's Admin
API exposure; any attack presupposing the attacker already holds the key.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md).

Trust boundaries: operator → SixSync (credential input), SixSync → Admin API
(TLS, `X-API-KEY` header only), SixSync → filesystem (atomic, 0600), SixSync →
logs (path-aware redaction). No other outbound connection exists.

## Supported versions

| APISIX | Tier | Status |
| --- | --- | --- |
| 3.18.x | 1 | Developed and integration-tested against 3.18.0 |
| 3.17.x | 2 | Planned for M4; untested |
| 3.16.x | 2 | Planned for M4; untested |
| ≤ 3.15 | — | Not supported. Defaults were written back on write before 3.16, which changes canonicalization fundamentally |

Compatibility is established by tests against pinned images, never by
documentation alone. Observed behaviour is recorded in
[docs/compatibility.md](docs/compatibility.md).

## Resource support matrix

| Kind | Status | Note |
| --- | --- | --- |
| Route | managed | read-only in this build |
| Service | managed | |
| Upstream | managed | |
| PluginConfig | managed | |
| GlobalRule | managed | APISIX has no delete-checker for this kind |
| Consumer | **refused** | credentials are returned in plaintext on read |
| Credential | **refused** | secret material by definition |
| SSL | **refused** | APISIX strips the private key on read, so it cannot round-trip |
| Secret | **refused** | holds credentials for the secret manager itself |
| StreamRoute | **refused** | requires stream proxy mode; untested |
| ConsumerGroup | **refused** | only meaningful alongside consumers |
| PluginMetadata | **refused** | instance-global; needs an ownership model |
| Proto | **refused** | grpc-transcode only |

Refused kinds are named and explained, never silently skipped.

## CLI model

`version`, `doctor`, `import`, `lint`, `validate`, `drift` today.
`plan`, `apply`, `rollback` in M2–M3.

Configuration precedence: flags > environment (`SIXSYNC_*`, then `APISIX_*`) >
file > defaults.

Authentication: an Admin API key sent only as the `X-API-KEY` header, optionally
with mTLS. A key file is preferred over an environment variable.

Secrets: kinds carrying credentials are refused; APISIX's `$secret://` and
`$env://` references are preserved verbatim and never resolved.

## Diff semantics

Canonicalize both sides, then compare digests. Rules are explicit, per kind, and
derived from observed behaviour; unrecognized shapes error rather than guess.
See [docs/resource-format.md](docs/resource-format.md#canonicalization).

## Update and concurrency semantics

PUT to stable identifiers only. Never PATCH — it merges maps rather than
replacing them, so the result depends on prior state instead of on the file.
Never POST — identity is decided in Git.

Concurrency is detect-then-abort on `modifiedIndex`, not compare-and-swap, which
APISIX does not offer. See
[ADR-0004](docs/decisions/ADR-0004-concurrency-via-modifiedindex.md).

## Deletion

Requires `lifecycle.state: absent` in a committed file, plus explicit
confirmation at apply time. A missing file is never a deletion. See
[ADR-0002](docs/decisions/ADR-0002-explicit-reviewed-deletion.md).

## Failure, recovery, and the limits of rollback

Apply stops at the first failure and attempts to restore already-applied
operations in reverse order from a pre-change journal.

Stated plainly: **this is not a transaction.** APISIX offers no multi-resource
atomicity, so a failure mid-apply can leave mixed state. Rollback is best-effort;
when it cannot complete, SixSync reports exit code 5 rather than implying
success. It also cannot undo the consequences of a change that was applied and
took effect — traffic already routed has been routed.

## Exit codes

`0` success or no differences · `1` failure · `2` differences with
`--detailed-exitcode` · `3` remote conflict · `4` policy denied · `5` rollback
incomplete · `64` usage. Codes 3–5 are reserved and currently unused.

## Testing strategy

Unit and table-driven tests throughout. Determinism tests that repeat an
operation and compare bytes. `httptest` coverage of every client branch,
including both empty-list shapes and each authentication outcome. Secret-safety
tests asserting credentials never appear in output.

Integration tests run against a disposable, exactly-pinned APISIX behind the
`integration` build tag. They never run by default and must never be pointed at
a real gateway.

Canonicalization rules require tests for **both** halves: equivalent encodings
compare equal, and genuine differences still differ. The second is what stops a
rule quietly hiding real drift.

## Release strategy

Semantic versioning. `v0.x` means the format may change, with migration notes.
Linux amd64 and arm64 first. GitHub Releases with checksums; SBOM and signing at
M4. No `latest` tag in any example.

## Internal dogfooding

The public tool stays organization-neutral. Internal use consumes public
releases; organization-specific policy and resource files live in a separate
private repository.

Adoption is staged: import offline and commit a baseline; run read-only drift
from a protected runner with a viewer key for at least a week; resolve
unexplained drift by hand; only then introduce a separate deployer key for a
single low-risk change. Scheduled automatic reconciliation is not enabled.

## Milestones

See [ROADMAP.md](ROADMAP.md). Each carries testable acceptance criteria.

## Risks

| Risk | Mitigation |
| --- | --- |
| A future APISIX stops returning `modifiedIndex` | `TestLiveEnvelopeShape` fails loudly; ADR-0004 records the dependency |
| APISIX resumes writing defaults back | `TestLiveDefaultsAreNotPopulated` catches it before diffs fill with noise |
| Mass deletion | No delete-by-omission; explicit tombstones; dual confirmation |
| A secret reaches Git | Credential-bearing kinds refused outright; redaction as backstop; lint heuristic |
| A shared plugin config change hits every route at once | Blast-radius reporting in M2 |
| Partial apply leaves mixed state | Journal, stop-on-first-failure, honest exit codes, no atomicity claim |
| Concurrent pipelines | `modifiedIndex` recheck; document CI concurrency groups |
| Confusion with an official ASF project | Trademark notice in README and NOTICE; no logo; no ASF name in the product name |
| Scope growth into an API management platform | Explicit non-goals; milestone gates |
| Single maintainer | Narrow scope; heavy tests; ADRs recording why, so the reasoning outlives the author |

## Deferred decisions

- Whether consumers can ever be supported without plaintext credentials in Git
  (M4, likely via `$secret://`)
- Whether an opt-in convergence mode is worth building
- Whether to adopt standalone API-driven mode for genuine conditional writes
- Whether policy rules need an expression language or a fixed set suffices
- Maximum acceptable plan age before apply

## Comparison with ADC

[api7/adc](https://github.com/api7/adc) is the incumbent, backed by the company
behind APISIX. **Most teams should use ADC.** SixSync exists for a narrower case.

| | ADC | SixSync |
| --- | --- | --- |
| APISIX backend maturity | *"experimental and still has many unresolved issues"* (its own README) | one kind of workflow, tested against 3.18.0 |
| Versions tested | 3.2–3.17 + `dev` | 3.18.0 |
| `upstream_id` references | not supported; upstreams are inlined | preserved |
| `plugin_config` references | not supported | preserved |
| Identifiers | `sha1(name)`-derived unless `--with-id` | APISIX identifiers preserved exactly |
| Layout | one aggregate file | one file per resource |
| Delete by omission | yes | never |
| Round-trip fidelity | *"not on our roadmap"* | the point |
| Rollback / plan artifact | none | M3 / M2 |
| Runtime | Node (Rust rewrite in progress) | single static Go binary |

The honest summary: ADC has broader coverage, real backing, and more tested
versions. SixSync is worth using if you need APISIX's reference model preserved
rather than inlined, existing identifiers kept, and deletion that cannot happen
by accident.

If ADC closes those gaps, contributing there beats maintaining this.

## Changes from the original plan

**Corrected by observation.** The original assumed APISIX populates defaults on
write and that this was the main source of false drift. It does not, since
3.16.0 — verified against a running instance. This removed a whole planned
subsystem: the per-version default table.

**Concurrency redesigned.** The original said "optimistic concurrency where
APISIX supports it". APISIX supports none. It does return `modifiedIndex`, so
the design became detect-then-abort, documented as such rather than implied to be
transactional.

**`apiVersion` group changed.** From `gitops.apisix.dev/v1alpha1` to
`sixsync/v1alpha1`. The original would have used an ASF product name as a
second-level domain, which the ASF trademark policy prohibits.

**Identity generalized.** The original assumed every resource is keyed by `id`.
Consumers are keyed by `username`, plugin metadata by plugin name, credentials
are nested. Identity is now per-kind.

**Refusals made explicit.** The original deferred consumers and SSL as scope. On
finding that APISIX decrypts `encrypt_fields` on read, deferral became a
requirement, and refused kinds now state their reason instead of being absent.

**Scope cut for v0.1.** Policy profiles, blast-radius reporting, route-collision
detection, rollback, fuzzing and performance work moved to M2–M4. v0.1 is
read-only and well tested rather than broad and shallow.

**Added.** Lint rules the original did not anticipate: `script` and
`filter_func` as remote code execution, and TTL routes as recurring drift.

**Simplified.** No `Project` file. `--repo-dir` and flags were enough; a project
file can arrive when something needs it.
