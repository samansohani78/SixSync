# APISIX GitOps Safety CLI — Project Plan

> Status: Draft v0.1  
> Working codename: **SixSync** (temporary; final name requires availability and trademark review)  
> Primary language: **Go**  
> Intended license: **Apache License 2.0**

## 1. Executive Summary

This project will provide a CI-provider-neutral, safety-first GitOps CLI for managing Apache APISIX resources through the official Admin API.

The tool is intended primarily for non-Kubernetes APISIX deployments where teams want Git to be the source of truth while retaining APISIX's traditional etcd-backed runtime and hot configuration updates.

Its main value is not merely converting YAML to Admin API requests. Its value is the operational safety layer around those requests:

- one file per APISIX resource;
- exact preservation of existing APISIX IDs and references;
- deterministic, reviewable plans;
- remote schema validation before mutation;
- explicit deletion semantics;
- optimistic concurrency checks;
- pre-deployment state capture;
- best-effort rollback after partial failure;
- drift detection without automatic overwrite;
- configurable policy enforcement;
- secret-safe output;
- first-class automation behavior for GitHub Actions, GitLab CI, Jenkins, and other CI systems.

The project will be built as a standalone open-source product first. The production APISIX environment that motivated the project will then be used as the first real-world dogfooding environment without introducing organization-specific behavior into the core.

## 2. Problem Statement

Apache APISIX provides a powerful Admin API and dynamic configuration model, but operating hundreds of Routes, Upstreams, Services, and Plugin Configs safely through a dashboard or ad hoc `curl` commands creates several problems:

- configuration changes are not consistently reviewed;
- the live gateway becomes the undocumented source of truth;
- accidental public Routes are difficult to detect;
- shared plugin changes may have a large, hidden blast radius;
- duplicate route match keys can be introduced silently;
- direct UI changes create configuration drift;
- deletions and partial failures are difficult to roll back;
- CI systems may expose Admin API tokens or secrets in logs;
- monolithic declarative files create merge conflicts at scale;
- generic synchronization can delete resources that were omitted accidentally.

The project will address these problems without writing directly to etcd and without requiring users to migrate APISIX to Kubernetes or standalone mode.

## 3. Positioning and Differentiation

### 3.1 Relationship to ADC

ADC already provides dump, lint, diff, validate, conversion, and sync workflows for API7 Enterprise and Apache APISIX. This project must not present itself as a replacement for every ADC use case.

The initial Apache APISIX backend documentation for ADC currently describes that backend as experimental and documents limitations such as:

- unsupported external `upstream_id` references;
- unsupported `plugin_config` references;
- normalization that inlines referenced resources;
- resources that may always appear changed;
- incomplete coverage of some resource types and APISIX versions.

This project's differentiation will therefore be narrow and explicit:

| Area | This project |
| --- | --- |
| Primary workflow | Safe CI/CD reconciliation of raw APISIX resources |
| Resource layout | One file per resource |
| Existing installations | ID-preserving adoption is a first-class workflow |
| APISIX fidelity | Preserve `upstream_id`, `service_id`, and `plugin_config_id` |
| Deletion | Explicit tombstone plus reviewed plan; never delete by omission by default |
| Concurrency | Revalidate remote hashes immediately before apply |
| Recovery | Pre-state journal and best-effort rollback |
| Policies | Configurable organizational checks |
| Drift | Detect and report; never overwrite automatically by default |
| CI providers | Provider-neutral CLI with optional adapters/examples |
| Secrets | Redacted output and runtime-only secret expansion |

If ADC later provides equivalent safety and full APISIX support, interoperability or contribution upstream should be considered instead of unnecessary competition.

### 3.2 Non-official Status

The project must clearly state that it is community-maintained and is not an official Apache Software Foundation project unless it is formally accepted in the future.

The README and documentation must include an appropriate Apache APISIX trademark notice.

## 4. Target Users

- SRE and platform teams running APISIX on VMs or bare metal;
- teams using APISIX traditional mode with etcd;
- organizations that require merge-request or pull-request review for gateway changes;
- operators with dozens or hundreds of routes;
- teams using GitHub Actions, GitLab CI, Jenkins, Drone, or similar systems;
- existing APISIX installations that need gradual adoption without resource recreation.

Kubernetes users whose complete workflow is already covered by APISIX Ingress Controller are not the primary v1 audience.

## 5. Product Principles and Safety Invariants

These are product contracts, not optional implementation details.

1. **Admin API only** — never write directly to etcd.
2. **Plan before apply** — non-interactive apply requires an immutable reviewed plan artifact.
3. **No deletion by omission by default** — missing files do not delete remote resources.
4. **Explicit ownership** — only resources within the selected scope are managed.
5. **Preserve stable IDs** — importing an existing environment must not replace resources with generated IDs.
6. **Remote state conflict detection** — apply aborts if relevant live state changed after plan.
7. **Fail closed on validation errors** — no partial apply begins after a failed local or remote validation.
8. **Capture pre-state** — every mutation is journaled with its prior canonical state.
9. **Stop on first mutation failure** — then attempt rollback of already-applied steps in reverse order.
10. **No secret values in output** — human output, JSON plans, logs, errors, and CI artifacts are redacted.
11. **No automatic drift overwrite by default** — emergency manual changes must not be silently reverted.
12. **Deterministic behavior** — identical desired and remote canonical states produce an empty plan.
13. **Version-aware behavior** — feature use is gated by the detected APISIX version.
14. **Least privilege documentation** — read-only operations should use a viewer key where APISIX permits it.
15. **No telemetry by default** — the CLI does not phone home.

## 6. Goals and Non-Goals

### 6.1 v0.x Goals

- import existing APISIX resources while preserving IDs;
- manage Routes, Upstreams, Services, Plugin Configs, and Global Rules;
- recursively load one-resource-per-file YAML or JSON;
- local structural validation;
- remote APISIX schema validation;
- dependency and route-collision analysis;
- canonical diff and plan creation;
- reviewed plan apply;
- explicit deletion workflow;
- drift detection;
- pre-state backup and best-effort rollback;
- policy profiles with warnings and deny rules;
- CI-safe human and JSON output;
- Linux amd64 and arm64 release binaries;
- official examples for GitHub Actions and GitLab CI.

### 6.2 Deferred Goals

- Consumers and credentials;
- SSL objects and private keys;
- Stream Routes;
- Plugin Metadata;
- Consumer Groups;
- OpenAPI conversion;
- templating or environment overlays;
- Kubernetes CRDs;
- distributed locking across unrelated CI systems;
- a web UI;
- hosted control plane;
- automatic secret-manager integrations;
- automatic promotion between environments.

### 6.3 Explicit Non-Goals

- replacing the APISIX runtime CLI;
- managing APISIX installation, systemd, etcd, firewall, or host configuration;
- replacing APISIX Ingress Controller;
- writing directly to etcd;
- making multi-resource Admin API changes fully atomic when APISIX itself has no such transaction;
- guaranteeing zero-risk rollback of an upstream application behavior change;
- silently resolving ambiguous route collisions;
- forcing one organizational security policy on all users.

## 7. Supported APISIX Modes and Versions

### 7.1 Initial Mode

- APISIX traditional mode;
- etcd-backed configuration;
- Admin API v3;
- non-Kubernetes deployments as the primary target.

### 7.2 Initial Compatibility Tiers

| APISIX version | Initial tier | Notes |
| --- | --- | --- |
| 3.18.x | Tier 1 | First development and dogfooding target |
| 3.17.x | Tier 2 | Planned CI compatibility target |
| 3.16.x | Tier 2 | Planned CI compatibility target |
| Earlier 3.x | Experimental | Community reports welcome; no initial guarantee |

Compatibility must be established by automated tests against pinned official APISIX container images, not only by documentation claims.

The CLI will run `doctor` to discover:

- APISIX version;
- Admin API reachability;
- key capabilities;
- remote schema validation availability;
- supported resource endpoints;
- authentication role suitability;
- TLS and CA configuration.

Unsupported or untested versions require an explicit override for mutation commands.

## 8. Configuration Model

### 8.1 Project File

Each repository contains a root project file:

```yaml
apiVersion: gitops.apisix.dev/v1alpha1
kind: Project

metadata:
  name: example-gateway

spec:
  resourcePaths:
    - environments/production/**/*.yaml

  ownership:
    labelKey: managed-by
    labelValue: sixsync

  policies:
    profile: policies/production.yaml

  lifecycle:
    deleteByOmission: false
    requireReviewedPlan: true
```

The final API group will be chosen after project naming is finalized.

### 8.2 Resource File

```yaml
apiVersion: gitops.apisix.dev/v1alpha1
kind: Route

metadata:
  id: "633380969088287422"
  name: payment-sub-users-export
  annotations:
    owner: payments
    access: protected

lifecycle:
  state: present

spec:
  name: payment-sub-users-export
  host: edge.example.com
  uri: /v1/api/report/payment/sub_users_list/export
  methods:
    - POST
    - OPTIONS
  status: 1
  upstream_id: "552218409505391294"
  plugin_config_id: auth-standard-v1
  plugins:
    proxy-rewrite:
      uri: /api/report/payment/sub_users_list/export
```

The `spec` object intentionally mirrors the APISIX Admin API resource body. The wrapper provides lifecycle, ownership, and policy metadata without forcing those fields into APISIX.

### 8.3 Explicit Deletion

```yaml
apiVersion: gitops.apisix.dev/v1alpha1
kind: Route

metadata:
  id: "633380969088287422"
  name: payment-sub-users-export
  annotations:
    deletion-reason: endpoint-retired

lifecycle:
  state: absent
```

Deletion additionally requires:

- `--allow-delete` during planning;
- a plan that visibly marks the deletion;
- `--allow-delete` during apply;
- successful dependency checks;
- a pre-state journal entry.

### 8.4 No Built-in Templating in v0.1

The core format remains deterministic. Users may render files using tools such as Helm, Kustomize, ytt, Jsonnet, or their own build step before invoking the CLI.

Native overlays may be considered later only if a clear use case cannot be met safely by pre-rendering.

## 9. CLI User Experience

Working binary name: `sixsync`.

```text
sixsync version
sixsync doctor
sixsync import
sixsync lint
sixsync validate
sixsync plan
sixsync apply
sixsync drift
sixsync rollback
sixsync completion
```

### 9.1 Example Workflow

```bash
sixsync doctor --server "$APISIX_SERVER"

sixsync lint \
  --project sixsync.yaml \
  --environment production

sixsync validate \
  --project sixsync.yaml \
  --remote

sixsync plan \
  --project sixsync.yaml \
  --out plan.json

sixsync apply \
  --plan plan.json \
  --non-interactive

sixsync drift \
  --project sixsync.yaml \
  --format json
```

### 9.2 Credentials

Credentials are accepted through environment variables or files:

```text
APISIX_ADMIN_API_URL
APISIX_ADMIN_API_KEY
APISIX_ADMIN_API_KEY_FILE
APISIX_CA_FILE
APISIX_CLIENT_CERT_FILE
APISIX_CLIENT_KEY_FILE
```

Command-line token flags should be discouraged because they can appear in shell history and process listings.

### 9.3 Output Modes

- concise human-readable output;
- JSON for CI systems;
- `--detailed-exitcode` for drift and plan;
- optional color with automatic TTY detection;
- deterministic ordering;
- no secrets;
- no HTTP authorization headers;
- no complete secret-bearing response bodies.

Proposed detailed exit codes:

| Code | Meaning |
| --- | --- |
| 0 | Success / no drift |
| 1 | Validation or operational failure |
| 2 | Changes or drift detected when detailed exit codes are enabled |
| 3 | Plan conflict: remote state changed after plan |
| 4 | Policy denied operation |
| 5 | Rollback incomplete |

## 10. Reconciliation Design

### 10.1 Import

`import` reads APISIX resources through the Admin API and produces individual resource files.

Requirements:

- preserve resource IDs exactly;
- preserve references rather than inline them;
- retain unknown valid APISIX fields;
- strip runtime fields such as create and update timestamps;
- redact or refuse secret-bearing resources by default;
- produce a machine-readable import report;
- never mutate the remote system;
- support include and exclude filters;
- create stable semantic filenames independent of IDs;
- detect filename collisions;
- support round-trip verification.

### 10.2 Canonicalization

Canonicalization must:

- sort object keys;
- preserve array order when APISIX semantics depend on it;
- normalize numbers without losing precision;
- remove known runtime-only fields;
- retain APISIX-populated defaults after baseline import when required for stable comparison;
- avoid treating YAML key ordering as a change;
- be version-aware where APISIX defaults differ.

Canonicalization behavior must be covered by golden tests.

### 10.3 Plan

A plan includes:

- tool version;
- APISIX version;
- project identity;
- environment;
- source commit when available;
- creation timestamp and maximum age;
- desired-state digest;
- canonical remote-state digests;
- create, update, delete, and unchanged operations;
- dependency order;
- policy results;
- redacted human diff;
- machine-readable operation bodies;
- a cryptographic plan digest.

Plan artifacts must be deterministic for identical inputs and state.

### 10.4 Apply

Apply consumes a plan rather than recalculating hidden changes.

Before the first mutation it must:

1. verify the plan digest;
2. verify plan age;
3. verify the desired-state digest;
4. fetch every affected remote resource again;
5. compare current hashes with the planned hashes;
6. abort on conflict;
7. validate delete flags;
8. create a pre-state journal.

Default creation and update order:

1. Plugin Configs;
2. Upstreams;
3. Services;
4. Routes;
5. Global Rules.

Default deletion order is reversed and includes dependency rechecks and bounded propagation waits.

The initial mutation method should be idempotent `PUT` to stable resource IDs. `PATCH` support may be added later only where it provides a verified safety benefit.

### 10.5 Optimistic Concurrency

APISIX Admin API does not provide a generic multi-resource transaction for this workflow. The CLI therefore provides optimistic concurrency:

- plan records canonical remote hashes;
- apply re-fetches relevant resources immediately before mutation;
- any mismatch aborts the entire apply before the first write;
- live changes after the first write remain possible and are recorded in the journal.

### 10.6 Rollback

Rollback is best effort and honest about its limitations.

- every successfully applied operation is appended to a durable local journal;
- on mutation failure, already-applied operations are restored in reverse order;
- rollback requests use the captured pre-state;
- rollback is itself validated and logged;
- incomplete rollback returns a distinct exit code;
- the tool never claims transactional atomicity;
- users can run an explicit rollback using a retained journal artifact.

## 11. Dependency Graph

The planner builds a graph for:

- Route to Upstream;
- Route to Service;
- Route to Plugin Config;
- Service to Upstream;
- future resource relationships.

The graph is used to:

- reject missing references;
- order creates and updates;
- prevent unsafe deletes;
- display blast radius;
- identify shared Plugin Config changes;
- warn when one resource change affects many Routes.

Example plan warning:

```text
HIGH BLAST RADIUS
plugin_config/auth-standard-v1 is referenced by 306 routes.
Updating this resource changes all referenced routes immediately.
Recommended action: create auth-standard-v2 and migrate in batches.
```

## 12. Policy System

The tool must separate generic safety invariants from organization-specific policy.

### 12.1 Built-in Checks

- duplicate resource IDs;
- duplicate names within a configurable scope;
- exact Route match collisions;
- missing references;
- unmanaged cross-scope references;
- unknown kinds;
- invalid lifecycle state;
- delete without explicit authorization;
- plan/apply state conflicts;
- secret-like values that would be printed or committed;
- unsupported APISIX version or feature.

### 12.2 Configurable Policy Profile

```yaml
apiVersion: gitops.apisix.dev/v1alpha1
kind: PolicyProfile

metadata:
  name: production

spec:
  rules:
    - id: route-public-requires-reason
      severity: deny
      config:
        annotation: access
        value: public
        requireAnnotations:
          - owner
          - public-reason

    - id: route-all-methods
      severity: deny

    - id: cors-wildcard
      severity: warn

    - id: plugin-config-blast-radius
      severity: warn
      config:
        routeThreshold: 20
```

Initial policy implementation should use a small set of documented built-in rules. A general-purpose expression language such as CEL may be considered later after the stable resource model is established.

## 13. Secrets and Sensitive Data

### 13.1 Core Rules

- no token in CLI arguments in recommended workflows;
- no token in plan or drift artifacts;
- no `set -x` in published CI examples;
- redact common credential field names recursively;
- redact Authorization, Cookie, certificates, private keys, passwords, tokens, and known plugin secret fields;
- reject plaintext secret-like fields in Git by default;
- permit runtime environment substitution without logging resolved values;
- support APISIX-native secret references without resolving them;
- treat imported Consumers, credentials, and SSL private keys as deferred unless explicitly enabled.

### 13.2 No Required Secret Manager

The project will not require Vault or another secret manager. Documentation will support:

- protected and masked CI variables;
- CI file variables;
- runner-provided secret files;
- environment variables;
- APISIX-native secret references;
- external secret managers as optional integrations.

## 14. CI/CD Integration

The core CLI remains provider-neutral.

### 14.1 Pull/Merge Request Pipeline

- format check;
- local lint;
- unit tests;
- static analysis;
- policy checks;
- optional ephemeral APISIX schema test;
- no production credential required.

### 14.2 Protected Default Branch Pipeline

- remote doctor;
- remote schema validation;
- plan against the selected environment;
- publish redacted plan artifact;
- require deployment approval according to CI provider capability;
- apply using a reviewed plan;
- smoke test;
- publish deployment journal;
- run post-apply drift check.

### 14.3 GitLab Example

- Protected Runner;
- runner locked to the deployment project;
- Protected and Hidden variables;
- Protected Environment;
- manual production deploy;
- `resource_group` for serialization;
- separate viewer and deployer API keys;
- plan and journal artifacts with controlled retention.

### 14.4 GitHub Actions Example

- protected environment;
- required reviewers;
- environment-scoped secrets;
- concurrency group per gateway environment;
- artifact retention for plans and journals;
- pinned action commit SHAs.

## 15. Drift Detection

`drift` compares Git desired state with remote canonical state without mutation.

Requirements:

- viewer credential support;
- label, ID, kind, and directory scoping;
- human and JSON output;
- detailed exit code;
- no automatic repair by default;
- identify unmanaged remote resources separately;
- identify missing desired resources separately;
- distinguish drift from unreachable Admin API;
- never expose secrets in drift output.

Future optional behavior may open an issue or send a webhook, but notification integrations are not required for v0.1.

## 16. Observability and Auditability

- structured logs using Go `slog`;
- optional JSON log mode;
- request IDs without credentials;
- duration and result for each Admin API operation;
- plan and apply summaries;
- plan digest, source commit, and tool version in journals;
- no phone-home telemetry;
- optional local metrics output deferred.

## 17. Go Architecture

```text
cmd/sixsync/
internal/
  apisix/
    client.go
    discovery.go
    schema.go
    resources.go
  canonical/
  config/
  dependency/
  diff/
  importcmd/
  journal/
  plan/
  policy/
  reconcile/
  redact/
  rollback/
  secrets/
  version/
pkg/
  model/
schemas/
docs/
examples/
  gitlab/
  github-actions/
testdata/
```

### 17.1 Implementation Guidelines

- Go standard library for HTTP and cryptography;
- minimal, pinned dependencies;
- YAML library with strict decoding;
- Cobra or an equivalently maintained CLI framework;
- context cancellation for all network operations;
- bounded timeouts;
- retries only for safe and idempotent operations;
- no retry of ambiguous failed mutations without confirmation;
- typed error categories;
- deterministic serialization;
- interfaces around Admin API client for testing;
- no global mutable state.

## 18. Testing Strategy

### 18.1 Unit Tests

- strict config parsing;
- canonicalization;
- redaction;
- dependency graph;
- policy evaluation;
- plan determinism;
- operation ordering;
- conflict detection;
- rollback ordering;
- exit codes.

### 18.2 Golden Tests

- imported resource files;
- human plan output;
- JSON plan output;
- canonical APISIX responses;
- redaction fixtures;
- policy reports.

### 18.3 Integration Tests

- mock Admin API failure modes;
- timeout and retry behavior;
- partial apply and rollback;
- resource conflict after plan;
- explicit delete and dependency protection;
- viewer versus admin key behavior.

### 18.4 APISIX Compatibility Tests

For each supported version:

1. start APISIX and etcd from pinned images;
2. wait for readiness;
3. create representative resources;
4. import them;
5. verify an empty round-trip plan;
6. test create, update, and explicit delete;
7. test Plugin Config and Upstream references;
8. test schema validation;
9. test rollback after injected failure;
10. publish a compatibility report.

### 18.5 Fuzzing

Use Go fuzz tests for:

- YAML and JSON parsing;
- canonicalization;
- redaction;
- resource filename generation;
- diff generation.

## 19. Repository Quality and Supply Chain

Initial repository files:

- `README.md`;
- `PROJECT_PLAN.md`;
- `ROADMAP.md`;
- `ARCHITECTURE.md`;
- `CONTRIBUTING.md`;
- `CODE_OF_CONDUCT.md`;
- `SECURITY.md`;
- `LICENSE`;
- `NOTICE`;
- `GOVERNANCE.md`;
- issue templates;
- pull request template;
- dependency update configuration;
- release workflow.

Quality gates:

- `go test ./...`;
- race detector where practical;
- `go vet`;
- `govulncheck`;
- `golangci-lint` with a reviewed configuration;
- CodeQL;
- secret scanning;
- dependency review;
- SBOM generation;
- checksums for every release artifact;
- signed release provenance when practical;
- pinned CI action versions.

## 20. License, Governance, and Contributions

### 20.1 License

Apache License 2.0 is the preferred license because it is permissive and aligns well with the Apache APISIX ecosystem.

### 20.2 Contribution Model

- public issues and discussions;
- pull requests with tests;
- Developer Certificate of Origin using `Signed-off-by`;
- conventional commits recommended but not mandatory initially;
- Architecture Decision Records for significant changes;
- security vulnerabilities reported privately according to `SECURITY.md`;
- a documented maintainer policy before v1.0.

### 20.3 Compatibility Governance

Adding a supported APISIX version requires:

- a green compatibility matrix;
- documented exceptions;
- representative import and round-trip tests;
- no known secret-redaction regression.

## 21. Release Strategy

- Semantic Versioning;
- `v0.x` means the file format and CLI may change with migration notes;
- stable `v1alpha1` configuration API during early releases;
- Linux amd64 and arm64 binaries first;
- macOS amd64 and arm64 next;
- container image published as an optional CI runtime;
- GitHub Releases with checksums and SBOM;
- release notes with APISIX compatibility table;
- no `latest` tag in production examples.

## 22. Milestones

### M0 — Product Definition and Repository Bootstrap

Deliverables:

- final project name;
- GitHub organization/repository decision;
- license and trademark notice;
- README problem statement;
- this project plan;
- architecture decision record template;
- Go module and basic CLI skeleton;
- contribution and security documents;
- CI for test, lint, and build.

Exit criteria:

- repository is public;
- clean build on Linux;
- no Admin API mutation capability yet.

### M1 — Read-Only Foundation (`v0.1.0`)

Deliverables:

- `version`;
- `doctor`;
- Admin API client;
- version discovery;
- import for Routes, Upstreams, Services, Plugin Configs, and Global Rules;
- strict YAML model;
- local lint;
- redaction engine;
- canonicalization;
- read-only drift;
- JSON and human output.

Exit criteria:

- import of a representative APISIX 3.18 environment succeeds;
- imported state produces an empty read-only plan;
- no secret appears in fixtures or output.

### M2 — Planning and Remote Validation (`v0.2.0`)

Deliverables:

- remote schema validation;
- dependency graph;
- collision detection;
- configurable policy profiles;
- deterministic plan artifact;
- blast-radius reporting;
- detailed exit codes.

Exit criteria:

- the same input and remote state produce byte-stable plan JSON;
- invalid APISIX plugin configuration is rejected before mutation;
- shared-resource blast radius is visible.

### M3 — Safe Apply and Recovery (`v0.3.0`)

Deliverables:

- reviewed-plan apply;
- create and update using stable IDs;
- optimistic concurrency;
- pre-state journal;
- partial-failure rollback;
- explicit deletion;
- dependency-aware operation ordering;
- GitLab and GitHub Actions examples.

Exit criteria:

- successful create/update/delete tests on APISIX 3.18;
- stale plan is rejected;
- injected mid-apply failure restores prior state in integration tests;
- incomplete rollback is reported honestly.

### M4 — Compatibility and Hardening (`v0.4.0`)

Deliverables:

- APISIX 3.16, 3.17, and 3.18 compatibility matrix;
- TLS and mTLS Admin API support;
- fuzz tests;
- performance testing with at least 5,000 resources;
- improved conflict and retry handling;
- signed releases, checksums, and SBOM.

Exit criteria:

- green published compatibility report;
- acceptable plan time and memory usage at target scale;
- no high-severity dependency vulnerability at release time.

### M5 — v1.0 Readiness

Deliverables:

- stable configuration API;
- documented migration policy;
- stable exit codes;
- stable journal and plan schemas;
- security review;
- maintainer and governance documentation;
- at least one non-author production adopter or equivalent external validation.

## 23. Internal Dogfooding Plan

The internal environment will consume public releases of the tool without private forks of the core.

Organization-specific policies, CI configuration, and resource files remain in a separate private repository.

### Phase A — Offline Baseline

- export current APISIX resources from the existing backup or viewer Admin API;
- create one file per resource;
- preserve all current IDs;
- run lint and collision analysis;
- commit a baseline tag;
- do not contact or mutate production from CI.

### Phase B — Read-Only Production

- install a protected CI Runner with Admin API network access;
- use a viewer key for doctor, import verification, plan, and drift;
- compare Git with production for at least one week;
- resolve unexplained drift manually;
- keep Dashboard write access unchanged until confidence is established.

### Phase C — Controlled Canary

- introduce a separate CI deployer key;
- protect the production environment;
- capture a fresh etcd snapshot outside the tool;
- apply one low-risk Route metadata-only change;
- verify traffic and drift;
- test rollback using the captured journal.

### Phase D — Git Becomes Source of Truth

- switch Dashboard users to a viewer key;
- require merge requests for all changes;
- allow break-glass changes only during incidents;
- require follow-up import/MR after a break-glass change;
- run scheduled drift detection;
- never enable scheduled automatic production reconciliation initially.

### Phase E — Configuration Cleanup

Cleanup is performed through small reviewed changes, not during initial adoption:

- classify intentionally public Routes;
- resolve exact Route match collisions;
- remove ineffective plugin fields;
- introduce versioned shared Plugin Configs;
- migrate Routes in canary batches;
- normalize labels and ownership metadata;
- remove unused Services, Upstreams, and Global Rules only after dependency confirmation.

## 24. Success Metrics

Product metrics:

- zero secrets in plan and log fixtures;
- deterministic plans in all supported compatibility tests;
- stale plans always rejected;
- no deletion without explicit lifecycle intent and flags;
- successful rollback in all injected supported failure scenarios;
- import round-trip produces no false drift on supported APISIX versions;
- plan of 5,000 resources completes within an agreed performance budget;
- CI examples require no vendor-specific runtime beyond the CLI.

Dogfooding metrics:

- 100% of managed production resources represented in Git;
- zero unexplained production drift after stabilization;
- every production mutation linked to a reviewed commit and plan artifact;
- no shared Plugin Config change deployed without blast-radius review;
- no direct Dashboard mutation outside documented break-glass events.

## 25. Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| APISIX default population creates false drift | Version-aware canonicalization and imported golden fixtures |
| Partial apply leaves mixed state | Pre-state journal, stop-on-failure, reverse rollback, honest exit codes |
| A shared Plugin Config changes hundreds of Routes | Dependency graph and blast-radius warning/deny threshold |
| Secret is included in a plan artifact | Recursive redaction, secret fixtures, security tests, default resource exclusions |
| UI or curl changes state after plan | Remote hash recheck immediately before apply |
| Accidental mass deletion | No delete by omission, tombstones, dual flags, explicit reviewed plan |
| Two CI systems deploy concurrently | Document CI concurrency groups; abort on state conflicts |
| APISIX API changes across versions | Feature discovery and container-based compatibility matrix |
| Project is confused with official Apache software | Clear community status and trademark notice |
| Scope grows into a full API management platform | Strict non-goals and milestone gates |

## 26. Open Decisions Before Coding

1. Final project and binary name.
2. GitHub user or organization that will own the repository.
3. Exact Go module path.
4. Whether v0.1 supports JSON input in addition to YAML.
5. Whether the wrapper API group should contain the final project domain or a neutral name.
6. Initial minimum Go version.
7. Exact plan and journal JSON schema versioning strategy.
8. Which APISIX-populated fields are removed during canonicalization for 3.18.
9. Whether a viewer key can access every endpoint required by `doctor`, `import`, and `drift` across supported versions.
10. Whether `GlobalRule` should be included in the first mutable release or remain read-only initially.
11. Maximum acceptable plan age before apply.
12. Default retry and propagation-wait policies.

## 27. Recommended Immediate Next Steps

1. Select a final name after GitHub, package registry, container registry, and trademark searches.
2. Open a public design discussion with this document.
3. Create three ADRs:
   - Admin API only;
   - explicit deletion and reviewed plans;
   - one resource per file with raw APISIX `spec`.
4. Bootstrap the repository without mutation features.
5. Implement read-only `doctor`, `import`, `lint`, and `drift` first.
6. Use the existing APISIX 3.18 backup as private test data only after generating a fully sanitized synthetic fixture.
7. Publish `v0.1.0` only after the read-only round-trip test is stable.

## 28. Reference Material

- Apache APISIX Admin API: <https://apisix.apache.org/docs/apisix/admin-api/>
- APISIX Dashboard: <https://apisix.apache.org/docs/apisix/dashboard/>
- ADC repository: <https://github.com/api7/adc>
- ADC APISIX backend limitations: <https://github.com/api7/adc/blob/main/libs/backend-apisix/README.md>
- GitLab deployment safety: <https://docs.gitlab.com/ci/environments/deployment_safety/>
- GitHub Actions environments: <https://docs.github.com/actions/deployment/targeting-different-environments/using-environments-for-deployment>

