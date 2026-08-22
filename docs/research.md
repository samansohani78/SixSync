# Research notes

Sources consulted while designing SixSync, and what came from each.

Two categories are kept apart deliberately, because during this work they
disagreed: what a source *says*, and what a running instance *does*. Where they
conflict, [compatibility.md](compatibility.md) records the observation and it
wins.

## Apache APISIX

### Primary sources

- Source at tag **`3.18.0`** (the tag is `3.18.0`, not `v3.18.0`):
  `https://raw.githubusercontent.com/apache/apisix/refs/tags/3.18.0/<path>`
  - `apisix/admin/init.lua` — routing table, `check_token`, `strip_etcd_resp`,
    `schema_validate`
  - `apisix/admin/resource.lua` — CRUD handlers, `check_conf`, SSL key masking
  - `apisix/admin/utils.lua` — `inject_timestamp`
  - `apisix/core/etcd.lua` — `set`, `push`, `atomic_set`, `kvs_to_node`
  - `apisix/schema_def.lua` — `id_schema` and every field default
  - `apisix/admin/{upstreams,services,plugin_config,consumer_group,proto}.lua` —
    `delete_checker`
  - `conf/config.yaml.example` — the commented-out `role: viewer` key
- Documentation at 3.18.0: `admin-api.md`, `deployment-modes.md`,
  `terminology/*.md`, `plugin-develop.md`, `control-api.md`, `status-api.md`,
  `CHANGELOG.md`
- <https://apisix.apache.org/docs/apisix/admin-api/>

A local copy of the 3.18.0 documentation tree may sit under
`third_party/apisix-docs-3.18.0/` during development. It is gitignored and never
published. It is Apache-2.0 material belonging to the ASF.

That copy has been verified against the official upstream tarball: 459 of 463
files are byte-identical to the 3.18.0 tag and the remainder match `master`.
See [vendored-docs-audit.md](vendored-docs-audit.md). It is still not treated as
authoritative — behaviour claims are verified against a running instance —
but it is genuine, unmodified Apache APISIX material.

### Findings that shaped the design

**Defaults are not written back (3.16+).** `CHANGELOG.md` records
`":warning: feat: admin api no longer populates default values when writing"`
(#12603), while `schema_def.lua` still declares defaults for `route.priority`,
`route.status`, `upstream.type`, `upstream.scheme` and others. Reading alone
could not settle which governed. Observation did: a route created with only
`uri` and `upstream_id` reads back with exactly those fields.

This is the single most consequential finding. It means SixSync needs no
per-version table of default values — the thing ADC's maintainers describe as
the blocker for clean diffs.

**Revision indices reach the client.** See
[ADR-0004](decisions/ADR-0004-concurrency-via-modifiedindex.md). The source
reading said they did not; the instance returns them on every read.

**`role: viewer` exists** and permits only `GET`
(`viewer_methods = {get = true}` in `init.lua`). All authentication failures are
`401`, distinguished only by a `description` field — which is why SixSync's
error classification matches on that string.

**Reference integrity is partial.** `delete_checker` covers upstreams, services,
plugin configs, consumer groups and protos. It does **not** cover routes, SSLs,
credentials, plugin metadata, secrets or global rules. Deleting a plugin config
that routes still reference is permitted and breaks those routes with a 503 at
request time — which is a concrete reason for SixSync's dependency checks.

**Literal `?force=true`** bypasses the checker entirely. SixSync never sends it.

**Secrets are decrypted on read.** `plugin-develop.md`: *"When fetching resources
… APISIX automatically decrypts the parameters declared in `encrypt_fields`"*,
with `data_encryption` on by default. This turned "defer consumers" from a
scoping choice into a requirement.

**`script` and `filter_func` execute Lua** on the gateway
(`admin-api.md` route parameters) — the basis of the `embedded-lua` lint rule.

**Routes can carry a TTL** and expire on their own — the `ephemeral-route` rule.

## ADC (api7/adc)

- <https://github.com/api7/adc>
- `libs/backend-apisix/README.md`
- `docs/guides/workflow.md`, `docs/reference/configuration.md`,
  `docs/guides/resource-ids.md`
- `.github/workflows/e2e.yaml` — the tested-version matrix

TypeScript/Node, distributed as a bundled single-executable binary, currently
being rewritten in Rust on an unmerged branch. Its CI matrix covers APISIX
3.2–3.17 plus `dev`; **3.18 is not in it**.

Quoted from its own APISIX backend README:

- *"This backend is experimental and still has many unresolved issues."*
- *"You cannot reference an external upstream in Route/StreamRoute/Service using
  `upstream_id`."*
- *"ADC does not support plugin templates"* (`plugin_configs`)
- *"Ensure that all referenced upstream is inlined into the resource that
  references it"*
- *"guarantees of absolute consistency in the form of resources are not on our
  roadmap"*
- *"Some resources will always be considered 'changed'"* — attributed to
  default-value population, and described as unsolvable *"unless we have a
  built-in table of default values for core resources and plugins for each
  version"*

Also: identifiers are `sha1(name)`-derived unless `--with-id`; `adc sync` deletes
resources absent from the local file; no rollback, no plan artifact, no policy
gates; consumer groups unsupported.

The last point is worth noting against the finding above — on APISIX 3.16+ the
default-population problem no longer exists, so clean diffs need observation of
current behaviour rather than a version table.

## Go and supply chain

- Go 1.27.0, released 2026-08-19. `go.mod` declares `go 1.25.0` with
  `toolchain go1.27.0`, so the module builds on both currently supported majors.
- **`gopkg.in/yaml.v3` is archived** (repository archived, last push 2025-04-01).
  SixSync uses `github.com/goccy/go-yaml`, actively maintained, with a
  first-class strict mode. Confirmed empirically to emit map keys in sorted
  order, which is what makes deterministic output possible.
- `spf13/cobra` v1.10.2 for the command tree.
- GitHub Actions are pinned to full commit SHAs. GitHub's own hardening guidance
  calls SHA-pinning the only way to use an action as an immutable release; a
  moved tag was the vector in the March 2025 `tj-actions/changed-files`
  compromise.
- `govulncheck` runs in CI via `go run golang.org/x/vuln/cmd/govulncheck@latest`.

## Trademark

- <https://www.apache.org/foundation/marks/>

Relevant constraints: ASF product names may not be used as second-level domain
names; a "confusingly similar software product name" is described as probably
infringing; the graphic logo may not be used except as a link to the project.

Consequences for SixSync: the name contains no ASF mark; the `apiVersion` group
is `sixsync/v1alpha1` rather than anything under `apisix.dev`; no APISIX logo is
used; and README and NOTICE both state the lack of affiliation.

## Verified locally, in this repository

- `apache/apisix:3.18.0-debian` and `quay.io/coreos/etcd:v3.5.33` both pull and
  run; see `deploy/compose/`.
- Every behaviour in [compatibility.md](compatibility.md) was observed against
  that stack.
- Name availability: no existing GitHub repository, Go module or Docker Hub
  image named `sixsync` at the time of writing.

## Not verified

- APISIX 3.16 and 3.17 behaviour. Declared Tier 2, untested.
- Consumer credential read-back. Documented, not reproduced — SixSync refuses
  the kind regardless.
- Pagination consistency under concurrent writes.
- mTLS against a real Admin API listener; only the client-side configuration
  path is covered by unit tests.
