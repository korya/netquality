# Guidelines

Process rules for changing this repository. Product behaviour lives in
`product-specs/`; architecture in `architecture.md`.

## Quality bar

- **Lint 100 %.** `go vet ./...` and `golangci-lint run ./...` (standard set,
  see `.golangci.yml`) must be clean. `gofmt -l .` must print nothing.
- **Test 100 % of use cases.** Every feature and scenario has a row in
  `test-matrix.md` naming its test; `TestMatrix` fails otherwise. Statement
  coverage is reported in CI, not gated.
- **Race-free.** `go test -race ./...` is the CI command; new concurrent code
  ships with a test that exercises it under the race detector.
- **Deterministic offline.** Tests run against the in-process server; only
  `TestLive` (`NQ_LIVE=1`, nightly) touches the Internet.

## Planning checklist (every non-trivial change)

1. Validate assumptions — read the code on `HEAD`, run a small experiment for
   any library or protocol behaviour you have not personally verified.
2. Cross-validate against product specs — list every affected spec ID
   (`DISC-n`, `LOAD-n`, …) and say how the change satisfies or alters it.
3. Cross-validate against `architecture.md` — layer boundaries, the
   assumptions table, and the invariants (`INV-n`).
4. Plan automated tests for new logic — unit where pure, in-process
   end-to-end otherwise; add matrix rows.
5. Plan end-to-end verification — which `nq` invocation or test proves it.

## Consultation rules

- Changing product behaviour → read the relevant spec first; update it in
  the same change.
- Changing structure or an assumption → read `architecture.md`; update it.
- Changing wire behaviour → check the draft; record any deviation in README
  "Deviations" (INV-6).
- Any conflict between a request and a spec, invariant, or the draft → raise
  it before writing code.
- **Every spec or architecture edit is confirmed with the user** before it is
  committed; specs lead implementation, not the reverse.

## Regressions

On a regression, run a causal analysis with the 5 whys and report the root
cause **before** attempting a fix. Distinguish symptom, proximate cause, and
root cause; check for siblings. Network flakiness on the developer machine
or CI runner is a frequent decoy — isolate with `curl` or a minimal program
before blaming the code.

## Submitting changes

- **Every change lands through a pull request**; nothing is pushed to
  `master` directly. Branch as `<author>-<slug>` or `<author>-<type>-<slug>`
  (e.g. `korya-docs-agent-docs`), open the PR as a draft, and merge only when
  CI is green and the description is current.
- Commits: small and reviewable; Conventional Commits subjects
  (`<type>(<scope>): <Subject>` — imperative, capitalised, ≤ 72 chars, no
  trailing period); body explains what and why, wrapped at 100 columns;
  co-author trailer when an agent authored it.
- PR description: `## Problem`, `## Solution`, optional `## Other Changes`.
  The first sentence of Problem and of Solution each state the essence in
  ≤ 25 plain words for the stakeholder concerned. Don't list files or paste
  diff; do cite spec IDs and issues in a `Related:` block.
- Before pushing: `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race ./...` clean for every touched package.
- If later commits change the PR's scope, update its title and body in the
  same push. CI failures are fixed at the root, never retried blindly.
- Tags and releases are cut from `master` after the PR merges.

## Releases

- Releases: move `[Unreleased]` in `CHANGELOG.md` under `## [X.Y.Z] - date`,
  commit, tag `vX.Y.Z`, push. The Release workflow builds, publishes, and
  triggers pkg.go.dev. Minor bump for features or compatibility changes,
  patch for fixes and docs.

## Documentation

Docs are code: reviewed in the same PR, kept short, cross-linked by ID.
See `README.md` in this directory for what goes where.
