# Contributing

## Before a large change

Open an issue first. SixSync has a narrow scope on purpose, and the fastest way
to have work rejected is to build something outside it. The non-goals in
[PROJECT_PLAN.md](PROJECT_PLAN.md) are the boundary.

## Development

```bash
make all              # format check, vet, test, build
make test-race
make lint             # requires golangci-lint
make vulncheck
```

Integration tests need a disposable APISIX:

```bash
make env-up
./scripts/seed-test-gateway.sh
make test-integration
make env-down
```

They are behind the `integration` build tag and never run as part of
`go test ./...`.

## The rule that matters most

**Never point a test, an example, or a development command at a gateway you care
about.** Integration tests write resources. The Compose stack has a wide-open
admin allowlist and published keys, and exists to be thrown away.

## Claims about APISIX behaviour

If a change depends on how APISIX behaves, verify it against a running instance
and record what you observed in [docs/compatibility.md](docs/compatibility.md).

This is not bureaucracy. During initial development, reading the 3.18.0 source
produced two confident conclusions that observation disproved — that
`modifiedIndex` never reaches a client, and that the v3 response format is
opt-in. Both were wrong, and both would have shaped the architecture badly.

Documentation and source are evidence. A running instance is proof. Cite the
exact APISIX version.

## Tests

Every behavioural change needs a test. In particular:

- **Determinism.** Anything producing output a human diffs or a pipeline parses
  must be byte-stable across runs. Map iteration in Go is randomized; run the
  operation repeatedly and compare.
- **Canonicalization.** A new rule needs cases proving both that equivalent
  encodings compare equal *and* that a genuine difference still differs. The
  second half is what stops a rule from quietly hiding real drift.
- **Secrets.** Anything touching credentials needs a test asserting the value
  does not appear in the output.

Table-driven tests are the house style. Name what the test protects, not what it
calls: `TestLargeIntegerReferencesKeepFullPrecision` over `TestCanonicalize3`.

## Code

- Standard library first. A dependency needs a reason beyond convenience.
- Comment *why*, not *what*. Most comments here explain a decision or cite
  observed APISIX behaviour.
- Per-kind knowledge belongs in `internal/model` and nowhere else.
- `internal/canonical` must never guess. An unrecognized shape returns an error;
  a confidently wrong diff is worse than a missing one.

## Commits and pull requests

Explain why, not just what. Sign off your commits (`git commit -s`) to certify
the [Developer Certificate of Origin](https://developercertificate.org/).

Never commit a real credential, hostname, IP address or production
configuration. Fixtures use synthetic values, `.example`/`.test` domains and
documentation-range addresses.

## License

Contributions are under the Apache License 2.0.
