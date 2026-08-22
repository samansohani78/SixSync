# Policy reference

A policy profile expresses an organization's limits. It is deliberately
separate from SixSync's own invariants — never deleting by omission, never
applying a stale plan — which are not configurable, because a tool whose safety
properties can be switched off does not have safety properties.

## Shape

```yaml
apiVersion: sixsync/v1alpha1
kind: PolicyProfile
metadata:
  name: production
spec:
  limits:
    max_creates: 50
    max_updates: 100
    max_deletes: 5
    max_total_changes: 150
    max_blast_radius: 20

  protected:
    ids:
      - GlobalRule/observability
      - PluginConfig/auth-standard-v1
    labels:
      tier: critical
    severity: deny

  embedded_lua: deny
  plaintext_secrets: warn
  require_delete_approval: true

  required_labels:
    - owner
  required_labels_severity: warn
```

Use it with `sixsync plan --policy policies/production.yaml`. The result is
recorded in the plan artifact, and `apply` refuses a plan that policy denied.

## Rules

| Rule | Configured by | Default |
| --- | --- | --- |
| `max-creates` | `limits.max_creates` | not configured |
| `max-updates` | `limits.max_updates` | not configured |
| `max-deletes` | `limits.max_deletes` | not configured |
| `max-total-changes` | `limits.max_total_changes` | not configured |
| `max-blast-radius` | `limits.max_blast_radius` | not configured |
| `protected-resources` | `protected.ids`, `protected.labels` | not configured; **deny** when set |
| `no-embedded-lua` | `embedded_lua` | **warn** |
| `no-plaintext-secrets` | `plaintext_secrets` | **warn** |
| `delete-requires-approval` | `require_delete_approval` | off |
| `require-labels` | `required_labels` | not configured; warn when set |

Severities are `off`, `warn` or `deny`. A numeric limit is always a denial when
exceeded: an operator who writes "at most 5 deletions" means it as a bound.

A limit of `0` means none are permitted, which is different from leaving it
unset.

## Notes on individual rules

**`max-blast-radius`** counts every resource a change reaches, following
references both directly and through intermediaries. It is the rule that
catches "this plugin config edit changes behaviour for 306 routes at once".
The violation message suggests creating a new version and migrating in
batches, because that is usually the right answer.

**`protected-resources`** defaults to `deny` when configured, since a
protected resource that only warns is not protected. `ids` are exact
`Kind/id` references; `labels` match any resource carrying that spec label.

**`no-embedded-lua`** fires on `script` and `filter_func`, which APISIX
executes as Lua on the data plane. Anyone who can merge a change to one has
remote code execution there, which is a different level of trust from editing
a route match.

**`no-plaintext-secrets`** is a heuristic on value shape — PEM armour, long
opaque tokens. It has false positives and false negatives, so it warns by
default. It is a check on human error, not a security control.

**`require-labels`** skips deletions, which cannot reasonably be required to
carry metadata.

## Why a fixed rule set

Rules are named and fixed rather than an embedded expression language. A rule
that reads at a glance is one a reviewer can reason about; an expression
language turns policy review into code review, and the policy file is often the
least-reviewed file in a repository.

If a rule you need is missing, an issue describing the decision it would
support is more useful than a general mechanism.
