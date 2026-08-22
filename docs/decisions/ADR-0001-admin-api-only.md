# ADR-0001: Manage APISIX only through the Admin API

**Status:** Accepted · 2026-08-23

## Context

APISIX stores its configuration in etcd. A tool could write there directly,
which would be faster, would allow genuine multi-resource transactions, and
would sidestep the Admin API's lack of conditional writes.

## Decision

SixSync uses the Admin API exclusively. It never writes to etcd, and never
requires etcd access for any normal operation.

## Why

Reading `apisix/admin/*.lua` at tag 3.18.0 shows the Admin API is not a thin
pass-through to etcd. Writing to etcd directly would bypass all of:

- **JSON-Schema validation.** `core.schema.check` runs before persistence; an
  invalid resource written straight to etcd is loaded by the data plane and
  fails at request time instead.
- **Field-level encryption.** Fields declared in a plugin's `encrypt_fields` are
  encrypted on write and decrypted on read. A direct writer would store
  plaintext where APISIX expects ciphertext, and the value would decrypt to
  nonsense.
- **Reference integrity.** `delete_checker` refuses to delete an upstream,
  service, plugin config, consumer group or proto that something still
  references, returning `400` and naming the referrer.
- **Timestamp bookkeeping.** `create_time` and `update_time` are injected
  server-side and are rejected outright if a client supplies them.

Each of these is a safety property SixSync would otherwise have to reimplement
and keep synchronized with APISIX forever. The APISIX version an operator runs
is not a version SixSync chooses.

There is also an operational argument: etcd credentials are strictly more
powerful than Admin API credentials. Requiring them would mean every CI runner
that checks for drift could rewrite the entire gateway.

## Consequences

**Accepted:** no multi-resource atomicity. The Admin API offers no transaction
across resources, so a partial failure mid-apply leaves mixed state. SixSync
will address this with ordering, a pre-change journal and honest reporting — not
by claiming atomicity it does not have.

**Accepted:** no compare-and-swap. See ADR-0004.

**Accepted:** SixSync is bounded by what the Admin API exposes. Where APISIX
does not enforce something — a plugin config can be deleted while routes still
reference it, breaking them with a 503 at request time — SixSync must catch it
locally instead.

**Rejected alternative:** a hybrid that reads from etcd for speed and writes
through the Admin API. It would double the compatibility surface for a
performance gain that no evidence suggests is needed.
