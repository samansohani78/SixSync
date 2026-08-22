# ADR-0002: Deletion is explicit, never inferred from absence

**Status:** Accepted · 2026-08-23

## Context

The usual declarative model is convergence: whatever is not in the desired state
gets removed. `adc sync` works this way — its documentation states that "a remote
resource that is in scope but absent from the local file can be deleted".

That model is coherent, and for a Kubernetes-style controller it is right. For a
gateway configured out of a Git repository it is dangerous.

## Decision

SixSync never deletes a resource because no file describes it. Deletion requires
a file that explicitly says so:

```yaml
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: pay-export-v1
lifecycle:
  state: absent
  reason: replaced by pay-export-v2 on 2026-08-01
```

A resource present in APISIX with no corresponding file is reported by `drift`
as **unmanaged** — a distinct category that is never a deletion candidate.

## Why

A file can be missing for many reasons, and almost none of them mean "delete the
production route":

- a merge resolved by taking one side
- a `git checkout` of a subdirectory, or a sparse checkout
- a path filter or `--repo-dir` pointed one level too deep
- a file never committed because it was still in `.gitignore`
- incremental adoption, where most of the gateway is not in Git yet

Under convergence, every one of these silently deletes live routes. The blast
radius is unbounded and the failure is immediate.

The asymmetry matters: failing to delete something is an untidy gateway that
someone notices later. Deleting something wrongly is an outage. Where the two
costs are that far apart, the default should not be the one that can page
someone.

Requiring the intent to be written down also puts it in the diff, where a
reviewer sees `state: absent` and a reason alongside the change.

## Consequences

**Accepted:** removing a resource takes two steps — mark it `absent`, then apply
— and a tombstone file lingers until someone removes it.

**Accepted:** SixSync cannot promise the gateway contains *only* what Git
describes. It can report what is unmanaged; closing that gap is a human
decision.

**Deferred:** an opt-in convergence mode for teams that genuinely want it. It
would have to be scoped by label or directory, never global, and it is not
needed to ship.

Mutation is not implemented in this build. This ADR fixes the semantics before
any code can accrete a different assumption.
