# Audit: the vendored Apache APISIX documentation

An earlier development note in this repository claimed that
`security-threat-model.md` in the vendored APISIX documentation was not genuine
upstream material and read as prompt-injection content. **That claim was wrong.**
This page records the verification that disproves it, so the error is not
repeated.

Performed 2026-08-23 against independently downloaded official sources.

## What was examined

`third_party/apisix-docs-3.18.0/` — a local, gitignored copy of the Apache
APISIX documentation kept for offline reference. No ZIP archive was found in the
repository or on the filesystem.

## Method

1. Downloaded the official source tarball for the tag directly from GitHub:
   `https://github.com/apache/apisix/archive/refs/tags/3.18.0.tar.gz`
   `sha256 5cf1e2b1cf57208b6b4a63cb64a853449501c3c7548348ea9af84cac3f7b0ca5`
2. Compared the entire tree file by file.
3. For every file that differed, compared it against `apache/apisix` `master`.
4. For files present locally but not under upstream `docs/`, compared against
   the upstream repository root.

## Result: genuine, unmodified Apache APISIX material

| Measure | Result |
| --- | --- |
| Files byte-identical to the 3.18.0 tag | **459 of 463** |
| Files differing from the tag | 4 — all byte-identical to `master` |
| Files present locally but not in upstream `docs/` | 11 — all byte-identical to the upstream repository root |
| Files not accounted for by an official upstream source | **0** |

### `security-threat-model.md`

```
sha256  99769b0ce92cf184c22cda72fe2a205ef9e7142962a1676cf9d6aaa241cc36ac
884 lines, 47813 bytes
```

**Byte-identical** to
`https://raw.githubusercontent.com/apache/apisix/refs/tags/3.18.0/docs/en/latest/security-threat-model.md`.
It is referenced by the upstream `config.json` doc index, and exists at tags
3.17.0 and 3.18.0 (added after 3.16.0, which returns 404).

### The four files that differ from the tag

All four match `apache/apisix` `master` exactly, so the tree was assembled from
a revision slightly newer than the 3.18.0 tag:

| File | Difference |
| --- | --- |
| `en/latest/admin-api.md` | one table cell: `retry_timeout` type `Integer` → `Number` |
| `en/latest/plugins/ai-proxy-multi.md` | upstream revision |
| `en/latest/plugins/graphql-limit-count.md` | upstream revision |
| `zh/latest/plugins/ai-proxy-multi.md` | upstream revision |

The `admin-api.md` change is a documentation correction and does not affect any
SixSync design decision.

### The eleven extra files

`AGENTS.md`, `CHANGELOG.md`, `CODE_OF_CONDUCT.md`, `CODE_STYLE.md`,
`CONTRIBUTING.md`, `MAINTAIN.md`, `README.md`, `SECURITY.md`, `THREAT_MODEL.md`,
`Vision-and-Milestones.md`, `powered-by.md`.

These live at the upstream **repository root**, not under `docs/`. Whoever
assembled the local copy merged both into one directory. Ten are byte-identical
to the 3.18.0 tag; `CHANGELOG.md` matches `master`.

`AGENTS.md` is genuine upstream content. It directs automated readers to
APISIX's threat model — which is a reasonable thing for a project to do, and is
not an instruction SixSync follows.

## Why the earlier claim was wrong

The earlier note reasoned that `security-threat-model.md` must be inauthentic
because it contained facts appearing nowhere else in the tree, and because it
tells a reader to treat certain classes of report as out of scope.

Both observations were correct; the inference was not. A threat model is exactly
the document that defines scope and records maintainer decisions, and it is
normal for it to be the only place a given detail appears. Absence of
corroboration elsewhere in a documentation tree is not evidence of forgery. The
only reliable test is comparison against the upstream source, which was not
performed at the time.

## Handling

- **Do not commit it.** `.gitignore` excludes `third_party/apisix-docs-3.18.0/`,
  verified with `git check-ignore`. It is Apache-2.0 material belonging to the
  ASF and redistributing it here would add ~470 unrelated files and APISIX logos
  to a project that must not be confused with an official one.
- **Do not treat it as authoritative.** Not because it is suspect — it is
  genuine — but because documentation describes intent while a running instance
  describes behaviour, and during this project the two have disagreed on points
  that mattered. Claims about APISIX behaviour are verified against a container
  and recorded in [compatibility.md](compatibility.md).
- **Keep it read-only and offline.** It is reference material, not input.

## Reproducing this audit

```bash
curl -sSL -o /tmp/apisix-3.18.0.tar.gz \
  https://github.com/apache/apisix/archive/refs/tags/3.18.0.tar.gz
sha256sum /tmp/apisix-3.18.0.tar.gz   # 5cf1e2b1cf57208b6b4a63cb64a853449501c3c7548348ea9af84cac3f7b0ca5
tar -C /tmp -xzf /tmp/apisix-3.18.0.tar.gz
diff -rq /tmp/apisix-3.18.0/docs third_party/apisix-docs-3.18.0
```
