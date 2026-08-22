# SixSync

GitOps for Apache APISIX, through the Admin API.

SixSync manages APISIX configuration declaratively, with Git as the source of
truth, for teams running APISIX on VMs or bare metal rather than Kubernetes.

> **This build is read-only.** No command changes remote state. Mutation
> (`plan`, `apply`, `rollback`) is planned but not implemented — see
> [ROADMAP.md](ROADMAP.md).

> SixSync is a community project. It is **not** affiliated with, endorsed by,
> or sponsored by the Apache Software Foundation. Apache, Apache APISIX and
> APISIX are trademarks of the ASF.

## Why this exists

Operating hundreds of routes through a dashboard or ad-hoc `curl` leaves the
live gateway as the only record of what is deployed. SixSync puts that record in
Git, and makes the difference between the two visible.

Its value is the safety around the requests, not the requests themselves:

- **Identifiers are preserved**, so an existing gateway can be adopted without
  recreating resources and interrupting traffic.
- **References stay references.** `upstream_id`, `service_id` and
  `plugin_config_id` are kept as written, never inlined into the referring
  resource.
- **Deletion is never inferred from a missing file.** A deleted file is far more
  often a bad merge or a partial checkout than an intent to remove a live route.
- **Diffs are honest.** Canonicalization rules are explicit and derived from
  observed APISIX behaviour, so field ordering, node encoding and server-set
  timestamps do not show up as phantom changes.
- **Secrets stay out of Git.** Resource kinds that carry credentials are refused
  rather than imported.

## Install

```
go install github.com/samansohani78/SixSync/cmd/sixsync@latest
```

Or build from source: `make build` produces `dist/sixsync`.

## Quick start

```bash
export SIXSYNC_ADMIN_URL=https://gateway.internal.example:9180
export SIXSYNC_ADMIN_KEY_FILE=/run/secrets/apisix-viewer-key

# What is on the other end, and what can this key do?
sixsync doctor

# Bring the live configuration into files, without overwriting anything.
sixsync import --repo-dir ./gateway --dry-run
sixsync import --repo-dir ./gateway

# Check the repository on its own. No endpoint, no credential.
sixsync lint --repo-dir ./gateway

# Where do the gateway and the repository disagree?
sixsync drift --repo-dir ./gateway
```

## Commands

| Command | What it does | Needs an endpoint |
| --- | --- | --- |
| `version` | Version, commit, build date, Go version | no |
| `doctor` | Reachability, TLS, authentication, key capability, APISIX version | yes |
| `import` | Read live resources into files, preserving identifiers | yes |
| `lint` | Check the repository on its own | no |
| `validate` | Structural checks; `--remote` also uses APISIX's schemas | optional |
| `drift` | Compare the repository with live state | yes |

## Configuration

Precedence, highest first:

1. Command-line flags
2. Environment (`SIXSYNC_*`, falling back to `APISIX_*`)
3. Configuration file (`--config`)
4. Defaults

| Setting | Flag | Environment |
| --- | --- | --- |
| Admin API URL | `--admin-url` | `SIXSYNC_ADMIN_URL` / `APISIX_ADMIN_API_URL` |
| Admin API key | `--admin-key` | `SIXSYNC_ADMIN_KEY` / `APISIX_ADMIN_API_KEY` |
| Key file | `--admin-key-file` | `SIXSYNC_ADMIN_KEY_FILE` |
| CA bundle | `--ca-file` | `SIXSYNC_CA_FILE` |
| Client certificate | `--client-cert-file` | `SIXSYNC_CLIENT_CERT_FILE` |
| Client key | `--client-key-file` | `SIXSYNC_CLIENT_KEY_FILE` |
| Request timeout | `--timeout` | `SIXSYNC_TIMEOUT` |
| Output format | `--output` | `SIXSYNC_OUTPUT` |
| Repository directory | `--repo-dir` | `SIXSYNC_REPO_DIR` |

Prefer `--admin-key-file`: a file can be mounted with restrictive permissions,
and unlike an environment variable it is not visible in `/proc` or inherited by
child processes. The key is sent only as the `X-API-KEY` header — never as a
query parameter, which would land in access logs.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, or no differences |
| 1 | Operational or validation failure |
| 2 | Differences found (only with `--detailed-exitcode`) |
| 3 | Remote state conflict *(reserved)* |
| 4 | Policy denied *(reserved)* |
| 5 | Rollback incomplete *(reserved)* |
| 64 | Usage error |

## Resource format

```yaml
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: pay-export              # the APISIX identifier, preserved exactly
  name: payments-export
lifecycle:
  state: present              # present | absent
spec:                         # verbatim APISIX Admin API body
  uri: /v1/report/payment/export
  methods: [POST, OPTIONS]
  service_id: payments-svc
```

`spec` is exactly what the Admin API accepts, so knowing APISIX is enough to
read a SixSync file. See [docs/resource-format.md](docs/resource-format.md).

## Supported resource kinds

Managed: **Route, Service, Upstream, PluginConfig, GlobalRule**.

Refused, each with a reason `sixsync doctor` will print: Consumer, Credential,
SSL, Secret, StreamRoute, ConsumerGroup, PluginMetadata, Proto. Refused kinds
are never silently skipped.

## Compatibility

Developed and tested against **Apache APISIX 3.18.0**. Behaviour was verified
against a running instance rather than inferred from documentation — including
several points where the two disagree. See
[docs/compatibility.md](docs/compatibility.md).

## Relationship to ADC

[api7/adc](https://github.com/api7/adc) covers much of this ground and is backed
by the company behind APISIX. Use ADC unless you specifically need what SixSync
does differently: preserved `upstream_id` / `plugin_config_id` references rather
than inlining, preserved APISIX identifiers rather than name-derived ones, and
no delete-by-omission. See [PROJECT_PLAN.md](PROJECT_PLAN.md#comparison-with-adc).

## Development

```bash
make all              # format check, vet, test, build
make env-up           # a disposable APISIX 3.18.0, pinned
make test-integration
make env-down
```

Integration tests require the disposable stack and never run by default. Point
them at a throwaway instance only.

## Documentation

- [PROJECT_PLAN.md](PROJECT_PLAN.md) — problem, scope, milestones
- [ARCHITECTURE.md](ARCHITECTURE.md) — how it is put together
- [ROADMAP.md](ROADMAP.md)
- [docs/compatibility.md](docs/compatibility.md) — observed APISIX behaviour
- [docs/security-model.md](docs/security-model.md)
- [docs/resource-format.md](docs/resource-format.md)
- [docs/research.md](docs/research.md) — sources
- [docs/decisions/](docs/decisions/) — architecture decision records

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
