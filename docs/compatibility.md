# Compatibility: observed APISIX behaviour

Every claim on this page was **observed**, not inferred. Each was produced by
issuing requests against a disposable container and recording the response
verbatim. Where reading the upstream documentation or source led to a different
conclusion, the observation wins and the discrepancy is called out.

## Support matrix

| APISIX | Tier | Integration suite | Notes |
| --- | --- | --- | --- |
| **3.18.0** | 1 | **31/31 pass** | Primary development target |
| **3.17.0** | 1 | **31/31 pass** | No behavioural differences found |
| **3.16.0** | 2 | **31/31 pass** | Two documented differences, below |
| ≤ 3.15 | — | not run | Unsupported: defaults were written back on write before 3.16, which changes canonicalization fundamentally |

Reproduce with `make compat-matrix`, or `./scripts/compat-matrix.sh 3.17.0` for
one version. Each version runs in its own Compose project on its own ports.

## Test environment

| Component | Pinned version |
| --- | --- |
| APISIX | `apache/apisix:{3.16.0,3.17.0,3.18.0}-debian` |
| etcd | `quay.io/coreos/etcd:v3.5.33` |
| Deployment mode | traditional, `config_provider: etcd` |
| `admin_api_version` | **not set** — whatever the shipped default is |

The stack uses host networking rather than published ports. Docker's port
forwarding goes through DNAT on the bridge, which some host firewall and VPN
configurations silently drop; host networking binds directly and is unaffected.

No production system was contacted at any point.

---

## Differences between supported versions

Two, both on 3.16.0. They are encoded as version-aware expectations in
`test/integration/compat_test.go` rather than smoothed over, so a change in
either direction fails the suite.

### 1. Schema validation is not role-gated on 3.16.0

`POST /apisix/admin/schema/validate/{kind}` accepts a `role: viewer` key on
3.16.0 and returns a schema verdict. APISIX 3.17.0 began requiring the admin key
there (#13328), so the same request returns
`401 invalid method for role viewer`.

**Consequence for SixSync.** `doctor`'s capability probe must not use this
endpoint, or every viewer key on 3.16.0 is reported as an administrator —
precisely the wrong answer for an operator trying to run least-privilege. The
probe instead issues a `DELETE` against an identifier APISIX can never store
(it contains a space, which the id schema rejects), so the request cannot
destroy anything, and the role check runs before dispatch on every version:

| | 3.16.0 | 3.17.0 | 3.18.0 |
| --- | --- | --- | --- |
| viewer → `DELETE /routes/<id with space>` | 401 role | 401 role | 401 role |
| admin → same | 404 Key not found | 404 | 404 |
| viewer → `POST /schema/validate/routes` | **400 schema** | 401 role | 401 role |

`validate --remote` therefore needs an administrator key on 3.17.0+, and works
with a viewer key on 3.16.0. That is a difference in APISIX, not in SixSync.

### 2. SSL private key material is returned on read by 3.16.0

`GET /apisix/admin/ssls` (the list endpoint) on 3.16.0 returns the `key` field
holding the private key **encrypted** under the `data_encryption` keyring.
APISIX 3.17.0 strips it from both single and list responses (#13227).

APISIX ships publicly-known default `data_encryption` keys, so unless an
operator has replaced the keyring, that ciphertext should be treated as
recoverable. This is worth knowing for anyone running 3.16.0 with a widely
readable Admin API.

**Consequence for SixSync.** None operationally: SSL is a refused kind on every
version, so key material never reaches a resource file. The finding reinforces
that refusal rather than changing it.

---

## Findings that contradict other sources

These four are the reason this page exists. Each was designed into SixSync one
way based on reading, then corrected after observation.

### 1. `modifiedIndex` and `createdIndex` ARE returned to clients

Reading `apisix/admin/init.lua@3.18.0` suggests otherwise: `strip_etcd_resp()`
sets `data.node.createdIndex = nil` and `data.node.modifiedIndex = nil`. That
strip applies to the **legacy `node`-wrapped** shape, which is not the shape
this build returns. Observed:

```json
{"createdIndex":28,
 "value":{"nodes":{"10.0.0.1:8080":1},"create_time":1787430328,
          "type":"roundrobin","update_time":1787430328,"id":"u-probe"},
 "key":"/apisix/upstreams/u-probe",
 "modifiedIndex":28}
```

**Consequence:** SixSync uses `modifiedIndex` as its concurrency token. This is
strictly stronger than a content digest, because it also detects a rewrite that
happens to produce identical content.

**Still absent:** any *conditional write*. There is no `If-Match`, and no
request field that carries an expected revision. `modifiedIndex` supports
detect-then-abort, never compare-and-swap. See ADR-0004.

### 2. The v3 response format is the default

Observed response header on every Admin API reply:

```
Server: APISIX/3.18.0
X-API-VERSION: v3
```

Lists use `{"list":[…],"total":N}`; single resources use the envelope above.
`?page=1&page_size=10` and `?name=` both return **200** without any
`admin_api_version` configuration.

### 3. Defaults are NOT written back — the most important finding here

A route created with only the two fields it requires:

```
PUT /apisix/admin/routes/r-probe   {"uri":"/probe","upstream_id":"u-probe"}
```

comes back from `GET` containing **only** what was sent, plus identity and
timestamps:

```json
{"update_time":1787430328,"uri":"/probe","create_time":1787430328,
 "upstream_id":"u-probe","id":"r-probe"}
```

No `priority`, no `status` — even though `apisix/schema_def.lua@3.18.0` declares
`priority` default `0` and `status` default `1`. Likewise an upstream created
with `type` and `nodes` returns no `hash_on`, `scheme`, `pass_host`, or
`keepalive_pool`.

This confirms the 3.16.0 changelog entry *"admin api no longer populates default
values when writing"* (#12603) as the authoritative behaviour: schema defaults
are applied at request-routing time, not persisted.

**Consequence:** SixSync does **not** need a per-version table of default values
to produce clean diffs on 3.16+. This removes a large, permanently-stale
maintenance burden — and it is the specific problem ADC documents as unsolved
(*"Some resources will always be considered 'changed'"*).

Untested here: 3.15 and earlier, where the old populate-on-write behaviour
presumably still applies. Those versions are out of scope.

### 4. The APISIX version IS discoverable from the Admin API

The `Server: APISIX/3.18.0` response header is present on Admin API responses.
Version discovery does **not** require the deprecated `server-info` plugin or
the loopback-only Control API on port 9090.

Treat it as best-effort: a reverse proxy may strip or rewrite `Server`.

---

## Confirmed behaviour

### Authentication and roles

`role: viewer` exists and works. All three failure modes return **401** and are
distinguished only by the `description` field:

| Condition | Status | Body |
| --- | --- | --- |
| no key | 401 | `{"error_msg":"failed to check token","description":"missing apikey"}` |
| unknown key | 401 | `{"error_msg":"failed to check token","description":"wrong apikey"}` |
| viewer key, non-GET | 401 | `{"error_msg":"failed to check token","description":"invalid method for role viewer"}` |

**Capability probe.** `POST /apisix/admin/schema/validate/routes` is a write-verb
request that mutates nothing. Observed:

- viewer key → `401` … `"invalid method for role viewer"`
- admin key → `400` with an `error_msg` describing the schema failure

This distinguishes a viewer key from an administrator key with **zero side
effects**, which is how `sixsync doctor` reports key capability.

### Schema validation

`POST /apisix/admin/schema/validate/{resource}` performs JSON-Schema checks only:

```
{"uri":1980}  → 400 {"error_msg":"property \"uri\" validation failed: wrong type: expected string, got number"}
{"uri":"/x"}  → 400 {"error_msg":"object matches none of the required: [\"plugins\",\"uri\"] or [\"upstream\",\"uri\"] or …"}
```

A route needs a `uri`/`uris` **and** one of `plugins`/`upstream`/`upstream_id`/
`service_id`/`script`. Note that passing validation does not guarantee the write
will succeed — reference existence is not checked here.

### Reference integrity on delete

```
DELETE /apisix/admin/upstreams/u-probe
→ 400 {"error_msg":"can not delete this upstream, route [r-probe] is still using it now"}
```

Enforced server-side for upstreams, services, plugin_configs, consumer_groups
and protos. **Not** enforced for routes, ssls, credentials, plugin_metadata,
secrets or global_rules. Literal `?force=true` bypasses the check entirely —
SixSync never sends it.

### Timestamps are server-owned

```
PUT {"type":"roundrobin","nodes":{…},"create_time":1}
→ 400 {"error_msg":"the property is forbidden:  create_time"}
```

(The doubled space is APISIX's, reproduced here verbatim.) `create_time` and
`update_time` cannot be written by a client, so the SixSync file format must not
carry them.

### An identical PUT is not a no-op

Writing byte-identical content twice, two seconds apart:

| | `update_time` | `modifiedIndex` |
| --- | --- | --- |
| after first PUT | 1787430328 | 31 |
| after second PUT | 1787430330 | 32 |

Both advance. **Consequence:** SixSync must skip resources it has determined are
unchanged rather than re-PUT them idempotently — a redundant write is visible
churn in etcd and invalidates any concurrent reader's `modifiedIndex`.

### `nodes` round-trips in whichever form was submitted

Map form in, map form out. Array form in, array form out — though **object key
order is not preserved**: `{"host","port","weight"}` came back as
`{"weight","port","host"}`.

Two consequences: canonicalization must normalize the two forms to one before
comparing, and comparison must never be done on serialized bytes.

### SSL private keys are stripped on read

`GET /apisix/admin/ssls/{id}` and the list endpoint both return only
`['cert','create_time','id','snis','update_time']` — no `key`, no `keys`.

**But `PUT` echoes the key back**, AES-encrypted under the `data_encryption`
keyring. Since APISIX ships with publicly-known default keys, treat that
ciphertext as sensitive. SixSync issues no PUTs today; this constrains M3.

---

### Large numeric identifiers are lossy in APISIX itself

A route referencing `upstream_id: 552218409505391294` as a JSON **number** is
rejected on every supported version:

```
{"error_msg":"failed to fetch upstream info by upstream id [5.5221840950539e+17], response code: 404"}
```

APISIX passes the value through a float, losing precision above 2^53. The same
digits as a **string** work correctly.

This is an APISIX limitation, not a SixSync one. SixSync preserves such values
exactly — it decodes with `json.Number` specifically so they are not corrupted —
and `lint` warns when a reference is a long numeric literal, advising that it be
quoted.

## Not yet verified

Recorded so they are not mistaken for established facts:

- Consumer credential read-back. `plugin-develop.md` states APISIX decrypts
  `encrypt_fields` when fetching, implying `GET /consumers/{name}` returns
  plaintext credentials. **Not yet reproduced.** SixSync refuses to import
  Consumers regardless, so this is a documentation gap rather than a risk.
- Behaviour on APISIX 3.16 and 3.17. Both are declared Tier 2 and untested here.
- Pagination consistency under concurrent writes.
- mTLS Admin API listener.
