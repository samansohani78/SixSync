# Security model

## What SixSync is trusted with

SixSync holds an Apache APISIX Admin API credential and reads the full
configuration of a production gateway. That configuration includes routing
rules, upstream addresses, and — for resource kinds SixSync deliberately refuses
— authentication credentials.

An Admin API key is equivalent to control of the gateway. There is no
finer-grained authorization in APISIX beyond the `viewer` / `admin` role split.

## Trust boundaries

| Boundary | Crossing | Protection |
| --- | --- | --- |
| Operator → SixSync | flags, environment, config file | key file preferred over an environment variable |
| SixSync → Admin API | HTTPS, `X-API-KEY` header | TLS verified by default; optional mTLS |
| SixSync → filesystem | imported resource files | atomic writes, mode 0600 |
| SixSync → terminal / CI logs | reports, errors | path-aware redaction; key never printed |

SixSync makes no other outbound connections. There is no telemetry, no update
check, and no crash reporting.

## Credential handling

**The key is sent only as the `X-API-KEY` request header.** APISIX also accepts
it as an `api_key` query parameter and as a cookie. Both are avoided
deliberately: a query parameter is recorded by access logs, proxy logs and
browser history, which is a common way an Admin API key escapes.

**Prefer `--admin-key-file`.** A file can be mounted with restrictive
permissions. Unlike an environment variable it is not visible in `/proc/<pid>/environ`
and is not inherited by child processes.

**The key is never printed.** Diagnostic output shows a truncated SHA-256
fingerprint instead, so an operator can confirm *which* key a pipeline picked up
without the value reaching a CI log. `TestStringNeverRevealsTheKey` enforces
this.

**Nothing derived from a secret's length or content is emitted.** The redaction
placeholder is a constant.

## Secrets in resource data

APISIX enables `data_encryption` by default and **decrypts `encrypt_fields` when
serving a read**. A `GET` on a consumer therefore returns plaintext credentials.

SixSync's response is not to redact them but to refuse the kinds that carry
them. Consumer, Credential, SSL, Secret and ConsumerGroup are recognized and
declined, each with a reason the tool will state. Redaction is a backstop for
what slips through, not the control.

Redaction itself is **path-aware rather than name-aware**. A rule on the field
name `key` would be both wrong and useless for APISIX: `ssl.key` is a private
key, `plugins.key-auth.key` is a credential, and `upstream.key` is a
consistent-hash input. Matching on position redacts the first two and leaves the
third alone.

`lint` additionally warns about values *shaped* like credentials — PEM armour, a
long opaque token. This is a heuristic with both false positives and false
negatives. It is a check on human error, not a security control.

## TLS

Server certificate verification is on by default, with a TLS 1.2 minimum.

`--insecure-skip-tls-verify` exists because some users have a throwaway
self-signed lab endpoint. It prints a warning to stderr on every run explaining
that the connection can be intercepted and the Admin API key read in transit.
The warning goes to stderr specifically so that it survives redirection of
machine-readable output.

mTLS is supported through `--client-cert-file` and `--client-key-file`. On APISIX
a client certificate is **additional** to the API key, never a replacement.

## What this build cannot do

No command changes remote state. Every Admin API request SixSync issues is
either a `GET` or a `POST` to `/apisix/admin/schema/validate/{kind}`, which
validates its request body and stores nothing.

`TestDiagnoseNeverMutates` asserts this for `doctor` by counting state-changing
requests against a fake server and requiring zero, for every key type.

## Least privilege

Use an APISIX key with `role: viewer` for read-only work:

```yaml
deployment:
  admin:
    admin_key:
      - name: viewer
        key: <a key you generated>
        role: viewer
```

`sixsync doctor` reports which kind of key it is given, using a probe that
mutates nothing (see [compatibility.md](compatibility.md)), and says so
explicitly when a key can write.

One caveat: `validate --remote` needs a key that may use `POST`, because APISIX
exposes schema validation only through a write method even though it changes
nothing. `lint` and `drift` work with a viewer key.

## Deliberate omissions

**No telemetry, ever.** Not opt-in, not anonymous. A tool holding gateway
credentials should make no outbound connection an operator did not ask for.

**No Vault or secret-manager integration.** Credentials come from CI variables,
environment variables or mounted files. APISIX's own `$secret://` and `$env://`
references are preserved in `spec` and never resolved by SixSync.

**No credential in a crash report,** because there are no crash reports.

## Reporting a vulnerability

See [SECURITY.md](../SECURITY.md).
