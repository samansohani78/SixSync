# Security policy

## Reporting a vulnerability

Report privately. Do not open a public issue for a security problem.

Use GitHub's private vulnerability reporting on this repository
(**Security → Report a vulnerability**), which is preferred because it keeps the
report and the fix in one place.

Please include:

- what an attacker can do, not only what is wrong
- the version or commit
- the APISIX version, if relevant
- reproduction steps, with **synthetic** values only

Never include a real Admin API key, hostname, or production configuration in a
report. If a real credential has already been exposed, rotate it first.

## What to expect

- Acknowledgement within a week.
- An assessment, with a severity and a plan, within two weeks.
- Credit in the release notes unless you prefer otherwise.

This is a small community project with no paid maintainers and no bounty
program. Timelines are best effort, and stated honestly rather than
aspirationally.

## In scope

- Disclosure of an Admin API key or other credential through any output path:
  logs, errors, reports, files written to disk.
- Secret material reaching a resource file.
- TLS verification being weaker than documented.
- Any command in a read-only build changing remote state.
- Deletion of a resource that was not explicitly marked `absent`.
- An apply proceeding when remote state has moved since the plan.

## Out of scope

- APISIX's own vulnerabilities. Report those to the
  [Apache APISIX project](https://apisix.apache.org/docs/general/security/).
- Consequences of `--insecure-skip-tls-verify`, which is documented, warned
  about on every run, and off by default.
- Consequences of an operator's own Admin API exposure or key management.
- Anything requiring an attacker who already holds the Admin API key. That is
  full control of the gateway by design.

## Supported versions

Pre-1.0. Only the latest release receives fixes.
