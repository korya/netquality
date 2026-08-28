# docs/

Documentation for humans and coding agents. `AGENTS.md` at the repo root is
the entry point and indexes everything here.

## What lives here

| Path | Content |
|---|---|
| `architecture.md` | Layers, key assumptions, technology choices, future paths |
| `guidelines.md` | Process: quality bar, planning checklist, consultation rules, regressions, releases |
| `product-specs/README.md` | Spec format rules and index of spec files |
| `product-specs/invariants.md` | System-wide properties, `INV-n` |
| `product-specs/<feature>.md` | Testable behaviours per feature area, `PREFIX-n` |
| `test-matrix.md` | Feature × scenario → test name, enforced by `TestMatrix` |

## Spec rules

- Behavioural, not implementational: no function, type, or file names.
- Testable as written; no separate "testable" lines.
- `### PREFIX-N: Title`; prefix fixed per file; numbers never reused or
  renumbered — deletions leave holes.
- Invariants are `INV-n` and live only in `invariants.md`.

## Process

- Treat docs as code: same PR, same review.
- Confirm every spec or architecture change with the user before committing.
- If implementation and spec disagree, surface it; do not silently edit the
  spec to match the code.
- Cite IDs in commit messages, PRs, tests, and code comments where a behaviour
  is deliberate (`// LIM-4: stop within ~200 ms`).

## When to update what

| Change | Update |
|---|---|
| Observable behaviour | The feature spec (new ID or edited body) + `test-matrix.md` |
| A cross-cutting rule | `invariants.md` |
| Structure, dependency, or assumption | `architecture.md` |
| How we work | `guidelines.md` |
| Deviation from the IETF draft | README "Deviations" (user-facing) and, if structural, `architecture.md` |
| Public API or CLI surface | README (user-facing) + the relevant spec |

## When not to write a doc

Implementation detail that a reader can get from the code, one-off
investigation notes, or anything already in the README. If it would not
change what someone builds or tests, leave it out.

## Naming

Lowercase, hyphenated, `.md`. One feature area per spec file, named for the
area (`load.md`, not `load-phases-spec.md`). Prefixes are short uppercase
tokens unique across the repo.
