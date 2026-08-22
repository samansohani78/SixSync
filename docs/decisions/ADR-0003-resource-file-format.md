# ADR-0003: One file per resource, wrapping a verbatim APISIX body

**Status:** Accepted · 2026-08-23

## Context

Three options were considered for how resources live on disk:

1. Raw Admin API JSON/YAML, one document per resource, no wrapper.
2. A single aggregate file listing everything, as ADC uses.
3. A thin wrapper around the raw body, one file per resource.

## Decision

Option 3. Each resource is a file:

```yaml
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: pay-export
  name: payments-export
lifecycle:
  state: present
spec:
  uri: /v1/report/payment/export
  methods: [POST, OPTIONS]
  service_id: payments-svc
```

`spec` is exactly what the Admin API accepts. The wrapper carries only what
APISIX cannot express: kind, stable identity, and intent.

## Why one file per resource

Merge conflicts scale with file size. A team with hundreds of routes editing one
aggregate file will conflict constantly, and a conflict in a file describing an
entire gateway is resolved under pressure with poor visibility. Per-resource
files confine a conflict to the resource that actually changed.

It also makes review legible: a pull request touching one file is visibly one
route.

## Why a wrapper rather than raw bodies

Three things have nowhere to live in a raw body:

- **Intent.** `lifecycle.state: absent` is the mechanism from ADR-0002. APISIX
  has no field for "should not exist".
- **Kind.** A raw body does not say what it is. Inferring kind from the
  directory makes moving a file change its meaning.
- **Format migration.** `apiVersion` is what lets the format change later
  without guessing at a file's vintage.

Keeping identity in `metadata` rather than `spec` avoids two places disagreeing
about the same value. `Validate` rejects a file that sets both.

## Why `spec` stays verbatim

An operator who knows APISIX can read a SixSync file with no translation layer,
and SixSync does not have to model APISIX's schema to round-trip a resource it
does not fully understand.

This is the concrete difference from ADC, which normalizes on export — inlining
referenced upstreams and plugin configs into the referring resource, and stating
that "guarantees of absolute consistency in the form of resources are not on our
roadmap". SixSync preserves what APISIX stores.

Unknown fields **inside `spec`** are preserved rather than rejected: APISIX gains
fields between releases, and a tool that dropped what it did not recognize would
corrupt configuration it was trusted to manage. Unknown fields at the **top
level** are rejected, because those are SixSync's own vocabulary and an
unrecognized one means a typo or a format mismatch.

## Excluded from `spec`

- `create_time` and `update_time` — APISIX rejects a write carrying them with
  `the property is forbidden:  create_time`, so a file containing them could
  never be applied.
- The identity field — it lives in `metadata`.

## Why the `apiVersion` group has no domain

`sixsync/v1alpha1`, not `gitops.apisix.dev/v1alpha1`.

The ASF trademark policy states that ASF product names may not be used as
second-level domain names. A group built on `apisix.dev` would assert exactly
that. It would also claim a domain this project does not control. A bare group
name asserts nothing.

## Consequences

**Accepted:** more files. A gateway with 500 routes has 500 files. Directories
are per kind, and file names are derived deterministically from name and
identifier, so they stay navigable.

**Accepted:** SixSync cannot validate `spec` contents on its own. That is
delegated to APISIX's schema endpoint via `validate --remote`, which is the
correct authority and does not rot as APISIX changes.

**Accepted:** the format is `v1alpha1` and may change before v1.0. `apiVersion`
is what makes a migration possible.
