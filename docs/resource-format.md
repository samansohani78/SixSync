# Resource format

Every value on this page is synthetic. Addresses come from documentation ranges
and hostnames from reserved domains.

## Shape

```yaml
apiVersion: sixsync/v1alpha1   # format version
kind: Route                    # which APISIX resource type
metadata:
  id: pay-export               # the APISIX identifier, preserved exactly
  name: payments-export        # human label, used in file names
  annotations:                 # operator notes; never sent to APISIX
    owner: payments-team
lifecycle:
  state: present               # present | absent
spec:                          # verbatim APISIX Admin API body
  uri: /v1/report/payment/export
```

| Field | Required | Notes |
| --- | --- | --- |
| `apiVersion` | yes | Must be `sixsync/v1alpha1`. |
| `kind` | yes | One of the supported kinds. |
| `metadata.id` | yes | 1–64 characters of `[a-zA-Z0-9._-]`, matching APISIX's own rule. |
| `metadata.name` | no | Label only. Mirrors APISIX's optional `name` where it has one. |
| `metadata.annotations` | no | Local to SixSync. |
| `lifecycle.state` | yes | `present` or `absent`. |
| `lifecycle.reason` | no | Why a resource is being removed. `lint` warns without it. |
| `spec` | when present | The Admin API body. Required unless `state: absent`. |

## Rules

**Unknown top-level fields are rejected.** They are SixSync's own vocabulary, so
an unrecognized one is a typo or a format mismatch.

**Unknown fields inside `spec` are preserved.** APISIX gains fields between
releases; dropping what SixSync does not recognize would corrupt configuration.

**`spec` must not contain the identity field, `create_time` or `update_time`.**
Identity lives in `metadata`; the timestamps are server-owned and APISIX rejects
a write that carries them.

**Identifiers are preserved exactly.** SixSync never derives an identifier from
a name, so renaming a resource does not recreate it.

**References stay references.** `upstream_id`, `service_id` and
`plugin_config_id` are kept as written, never inlined.

## File layout

```
gateway/
├── routes/
│   ├── payments-export-pay-export.yaml
│   └── reports-list.yaml
├── services/
│   └── payments-payments-svc.yaml
├── upstreams/
│   ├── payments-backend.yaml
│   └── reports-backend.yaml
├── plugin_configs/
│   └── auth-standard-v1.yaml
└── global_rules/
    └── observability.yaml
```

Directories are the kind's Admin API path. File names are
`<slug(name)>-<slug(id)>.yaml`, collapsing to `<slug(id)>.yaml` when they would
repeat. The identifier is always present, so two resources sharing a label
cannot collide.

## Examples

### Route

```yaml
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: pay-export
  name: payments-export
lifecycle:
  state: present
spec:
  name: payments-export
  uri: /v1/report/payment/export
  methods:
    - POST
    - OPTIONS
  service_id: payments-svc
  plugin_config_id: auth-standard-v1
  plugins:
    proxy-rewrite:
      uri: /api/export
```

### Service

```yaml
apiVersion: sixsync/v1alpha1
kind: Service
metadata:
  id: payments-svc
  name: payments
lifecycle:
  state: present
spec:
  name: payments
  desc: payments service
  upstream_id: payments-backend
```

### Upstream

Both node encodings are accepted and compare equal:

```yaml
apiVersion: sixsync/v1alpha1
kind: Upstream
metadata:
  id: payments-backend
lifecycle:
  state: present
spec:
  type: roundrobin
  scheme: http
  retries: 2
  nodes:
    10.20.0.11:8080: 5
    10.20.0.12:8080: 5
```

```yaml
apiVersion: sixsync/v1alpha1
kind: Upstream
metadata:
  id: reports-backend
lifecycle:
  state: present
spec:
  type: chash
  hash_on: vars
  key: remote_addr          # a hash input, not a credential
  nodes:
    - host: 10.20.1.5
      port: 9090
      weight: 1
```

### PluginConfig

```yaml
apiVersion: sixsync/v1alpha1
kind: PluginConfig
metadata:
  id: auth-standard-v1
lifecycle:
  state: present
spec:
  desc: shared auth and tracing
  plugins:
    cors: {}
    request-id: {}
```

A plugin config is shared. Changing one affects every route referencing it, at
once. Prefer creating `auth-standard-v2` and migrating routes in batches.

### GlobalRule

```yaml
apiVersion: sixsync/v1alpha1
kind: GlobalRule
metadata:
  id: observability
lifecycle:
  state: present
spec:
  plugins:
    prometheus: {}
```

A global rule applies to **every** request. APISIX has no delete-checker for
this kind, so nothing on the server side will warn you.

### Consumer — refused, shown for illustration

```yaml
# SixSync REFUSES this kind. It does not import or manage it.
apiVersion: sixsync/v1alpha1
kind: Consumer
metadata:
  id: partner-integration      # consumers are keyed by username, not id
lifecycle:
  state: present
spec:
  username: partner-integration
  plugins:
    key-auth:
      key: THIS-WOULD-BE-A-REAL-CREDENTIAL   # ← the problem
```

APISIX enables `data_encryption` by default and decrypts `encrypt_fields` when
serving a read, so `GET /apisix/admin/consumers/{name}` returns this value in
plaintext. Importing consumers would write live credentials into Git.

### Deletion

```yaml
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: pay-export-v1
lifecycle:
  state: absent
  reason: replaced by pay-export-v2 on 2026-08-01
```

No `spec` is needed. A missing file never means deletion — see
[ADR-0002](decisions/ADR-0002-explicit-reviewed-deletion.md).

## Canonicalization

Two files describing the same configuration compare equal even when written
differently:

| Difference | Reported as drift? | Why |
| --- | --- | --- |
| `create_time` / `update_time` | no | server-owned; rewritten on every write |
| `upstream_id: 1` vs `"1"` | no | APISIX echoes the submitted type |
| `methods` reordered | no | a set |
| `hosts`, `uris` reordered | no | sets |
| `vars` reordered | **yes** | an ordered expression list |
| node map form vs array form | no | documented as equivalent |
| node order | no | selection is by balancer, not position |
| node weight changed | **yes** | a real change |
| `priority: 0` added | **yes** | explicit, and APISIX 3.16+ stores no defaults |

Each rule has tests proving both halves: that equivalent forms compare equal,
and that a genuine difference still differs. See
[compatibility.md](compatibility.md) for the observations behind them.
