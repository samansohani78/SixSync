# Architecture

## Shape

```
cmd/sixsync            binds the process to internal/cli; nothing else
internal/
  cli/                 command definitions, flag binding, rendering
  config/              flag > env > file > default resolution
  apisix/              Admin API client: transport, envelope, errors, probing
  model/               the kind registry — all per-kind knowledge
  resource/            on-disk format, repository loading, atomic writes
  canonical/           per-kind normalization and deterministic digests
  lintrules/           repository checks that need no gateway
  redact/              path-aware redaction
  buildinfo/           version metadata injected at link time
  exitcode/            the exit-code contract
test/integration/      behind the `integration` build tag
deploy/compose/        a disposable APISIX, pinned to an exact version
```

Dependencies point one way: `cli` → everything, `model` → `redact`, and nothing
imports `cli`. Only `cmd` knows the process can exit.

## The kind registry

`internal/model` is the single place that knows anything type-specific: the
Admin API path, which field carries identity, which verbs APISIX implements,
which fields hold credentials, which fields reference other resources, and
whether SixSync manages the kind at all.

Everything else asks it. This is what keeps a `switch kind` from appearing
independently in the client, the importer, the linter and the canonicalizer —
the failure mode where adding a resource type means finding nine places and
missing two.

The registry also carries kinds SixSync *refuses*, each with a reason. A refused
kind is named and explained rather than silently skipped, because silently
skipping one leaves an operator believing their gateway is fully represented in
Git when it is not.

## The Admin API client

`internal/apisix` owns the wire format completely. It decodes the response
envelope, follows pagination, classifies failures, and hands callers a plain
`Resource`. No other package knows what an Admin API response looks like.

Three details are load-bearing and each came from observing a real 3.18.0
instance:

**`modifiedIndex` is captured.** It is SixSync's concurrency token — see
[ADR-0004](docs/decisions/ADR-0004-concurrency-via-modifiedindex.md).

**Numbers are decoded with `UseNumber`.** Plain `json.Unmarshal` turns every
number into a `float64`, and an APISIX reference such as `552218409505391294`
exceeds float64's exact-integer range. It would come back subtly corrupted, and
SixSync would then write the corrupted value back.

**An empty list can arrive as a JSON object.** `GET /apisix/admin/ssls?page=1&page_size=500`
on an empty collection returns `{"list":{},"total":0}` — Lua's empty table
serializes as `{}`. Decoding straight into a slice fails, which would break every
command against a gateway that has no resources of some kind: the first-run
experience.

Errors are classified into kinds (`auth-invalid`, `auth-insufficient`,
`referenced`, `conflict`, `validation`, …) so callers branch on cause rather than
on a status code. APISIX makes this necessary: all three authentication outcomes
are `401` and differ only in a `description` field, and a refused delete and a
schema rejection are both `400`.

## Canonicalization

`internal/canonical` decides whether two resources mean the same thing.

**There is no generic map-comparison fallback.** APISIX resources contain fields
whose JSON representation varies without their meaning varying: upstream nodes
accept two entirely different shapes, some lists are sets and others are ordered,
and reference fields echo back whatever numeric type the writer used. A generic
comparison reports all of that as drift — and an operator who sees phantom
changes stops reading diffs, which defeats the point of the tool.

So every rule is explicit and justified, and an unrecognized shape produces an
error rather than a guess. A confidently wrong diff is worse than a missing one.

Notably absent: any injection of default values. APISIX 3.16+ does not write
schema defaults back, confirmed by observation, so SixSync does not need the
per-version default table that ADC's maintainers describe as the blocker for
clean diffs.

Digests are computed over canonical JSON — sorted keys, no HTML escaping,
numbers written verbatim — so they are stable across runs and readable in golden
files.

## Determinism

Anything a human diffs or a pipeline parses must be byte-stable. Go randomizes
map iteration, so this is a property that has to be built and then tested for,
not assumed:

- resources sort by kind then identifier
- list results sort by etcd key
- lint findings sort by path then rule
- canonical JSON sorts object keys
- YAML output relies on `goccy/go-yaml` sorting map keys, verified empirically

Several tests run an operation 20–200 times and compare bytes.

## Redaction

`internal/redact` matches on a field's **position**, not its name. A rule on the
name `key` would be both wrong and useless for APISIX: `ssl.key` is a private
key, `plugins.key-auth.key` is a credential, and `upstream.key` is a
consistent-hash input.

The package has no APISIX knowledge; `model` supplies the policy. That keeps it
independently testable and avoids an import cycle.

Redaction is a backstop. The primary control is refusing to import kinds that
carry credentials at all.

## Error and exit-code flow

Commands return errors. `exitcode.Wrap` attaches a code where the default of
"generic failure" is wrong. `cli.Execute` is the single place an error becomes an
exit status.

Codes `3`, `4` and `5` are reserved and documented but unused, so a future
command cannot claim a number that pipelines already read as meaning something
else.

## What is deliberately absent

**No plugin system.** Extension points are for problems you have.

**No abstraction over the Admin API.** There is one backend. An interface for a
hypothetical second one would be shaped by guesses.

**No telemetry, no update check, no crash reporting.** A tool holding gateway
credentials makes no outbound connection the operator did not ask for.

**No write path.** Not stubbed, not partially present. `plan` and `apply` do not
exist as commands, so no one can discover them half-built and assume they work.
