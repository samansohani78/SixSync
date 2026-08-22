# ADR-0004: Conflict detection via `modifiedIndex`, not compare-and-swap

**Status:** Accepted · 2026-08-23

## Context

Two CI pipelines, or a pipeline and someone using the dashboard, can act on the
same resource at once. A tool that reads state, computes a change, and writes it
back has a window in which the world moves underneath it.

The ideal fix is a conditional write: "apply this only if the resource is still
at revision X". The question was whether APISIX offers one.

## Investigation

Reading `apisix/admin/init.lua` at tag 3.18.0 suggested revision information
never reaches a client at all: `strip_etcd_resp()` explicitly clears
`createdIndex` and `modifiedIndex`, and `run()` never forwards response headers.

**Observation contradicted that.** Against `apache/apisix:3.18.0-debian`:

```json
{"createdIndex":28,
 "value":{…},
 "key":"/apisix/upstreams/u-probe",
 "modifiedIndex":28}
```

The strip applies to the legacy `node`-wrapped shape, which is not what this
build returns. Both indices are present on every read, single and list.

Conditional *writes*, however, genuinely do not exist. There is no `If-Match`,
and no request field carrying an expected revision. `core.etcd.atomic_set` does
perform a real etcd compare-and-swap, but only inside a single `PATCH`, using a
revision the server read microseconds earlier. A client can neither supply one
nor observe the comparison.

## Decision

SixSync records `modifiedIndex` when it reads a resource, and re-reads it
immediately before writing. A change aborts the operation before the first
write, with exit code 3.

This is **detect-then-abort**, not compare-and-swap, and the documentation says
so plainly.

## Why `modifiedIndex` rather than a content digest

A digest detects a change in content. `modifiedIndex` detects a *write*, whether
or not it changed anything — confirmed by observation: writing byte-identical
content advanced `modifiedIndex` from 31 to 32 and `update_time` with it.

That is conservative in the right direction. If something else is writing to a
resource, the fact that it happened to write the same bytes is not reassurance;
it means a second actor is operating on state SixSync is about to change.

SixSync records the canonical digest too, for the plan artifact and for `drift`.
`modifiedIndex` decides whether to abort.

## Consequences

**Accepted:** a race window remains, between the re-read and the write. It is
milliseconds rather than the minutes between planning and applying, but it is
not zero, and SixSync will not claim otherwise.

**Accepted:** false conflicts. Any write to a resource — including one that
changed nothing — aborts the apply. The remedy is to re-plan. Aborting too often
is a nuisance; overwriting a concurrent change is an incident.

**Accepted:** SixSync must skip resources it knows are unchanged rather than
re-writing them idempotently, because a redundant write is real churn in etcd
and invalidates every concurrent reader's `modifiedIndex`.

**Watched:** if a future APISIX stops returning `modifiedIndex`, this design
must change. `TestLiveEnvelopeShape` in the integration suite fails loudly if it
disappears.

**Not adopted:** standalone API-driven mode, which *does* offer conditional
writes via `<resource>_conf_version`, per-resource `modifiedIndex` comparison and
an `X-Digest` header. It replaces the whole configuration in one call, exists for
the ingress controller, and the APISIX documentation warns against using it
directly. Worth revisiting; not for v0.1.
