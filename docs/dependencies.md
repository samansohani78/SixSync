# Dependencies

SixSync depends on four modules at runtime. Everything else comes from the Go
standard library — HTTP, TLS, JSON, cryptography and file handling are all
stdlib, because those are exactly the places where a dependency's bug becomes
this project's security problem.

| Module | Version | License | Why |
| --- | --- | --- | --- |
| `github.com/goccy/go-yaml` | v1.19.2 | MIT | YAML with strict decoding |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 | Command tree |
| `github.com/spf13/pflag` | v1.0.10 | BSD-3-Clause | Flag parsing, required by cobra |
| `github.com/inconshreveable/mousetrap` | v1.1.0 | Apache-2.0 | Windows support, required by cobra |

All four are compatible with Apache-2.0, this project's licence.

## Why `goccy/go-yaml` and not `gopkg.in/yaml.v3`

`gopkg.in/yaml.v3` was archived by its author in April 2025 and is no longer
maintained. A parser handling untrusted input needs someone fixing it.

`goccy/go-yaml` is actively maintained, offers first-class strict decoding —
SixSync rejects unknown top-level fields, so this is load-bearing — and was
verified to emit map keys in sorted order, which is what makes deterministic
output possible at all.

## Why cobra

A command tree with subcommands, generated help and shell completion, at the
cost of two small transitive dependencies. The standard library's `flag` has no
subcommand support, so the alternative was hand-rolling a dispatcher — more
code in this repository, doing a solved thing less well.

## Keeping the surface small

A dependency needs a reason beyond convenience. Anything touching credentials,
TLS, hashing or the wire format stays in the standard library, so a supply-chain
compromise would have to reach Go itself rather than a package nobody watches.

## Verification

- `go mod verify` runs before every release build.
- `govulncheck` runs on every push and before every release.
- Dependabot proposes updates weekly, grouped for routine bumps so a security
  fix arrives on its own rather than buried in a batch.
- Every release archive ships an SPDX SBOM, so a consumer can check exactly
  what they downloaded.
