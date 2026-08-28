# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `nqserver` bearer-token authentication (`--auth-token` / `NQSERVER_AUTH_TOKEN`)
  on every endpoint, and load limits that gate requests without shaping
  traffic: per-client byte budget (`--client-bytes`/`--client-window`, default 8 GiB
  per 10 min, unlimited under `--self-signed`; 429 + `Retry-After`),
  `--upload-size`, `--max-connections` (#1).
- `Options.Header` applied to every request; `nq --auth-token` /
  `NQ_AUTH_TOKEN`.
- Internal: `internal/linksim`, a fluid link model, and an algorithm
  scenario matrix with honesty/budget/convergence/accuracy oracles and a
  `knownFailing` ledger of what the current algorithm cannot yet do.

### Changed
- `nqserver` refuses to serve anonymously with a real certificate unless
  `--allow-anonymous`; `--self-signed` implies it.
- Internal: the per-interval measurement logic now lives in a pure,
  clock-free `internal/engine` package driven by observations, so it can be
  tested against recorded and simulated links. No behaviour or JSON change;
  public latency types are aliases of the engine's.

## [0.2.1] - 2026-08-28

### Added
- `target.local_ips`: the local source addresses the test's connections used,
  for correlating a stored result with the network it was taken on (#5).

### Changed
- `target.resolved_ips` (and `local_ips`) are now populated on cancelled
  partial results as well.

## [0.2.0] - 2026-08-28

### Changed
- Minimum Go version is 1.26. Go 1.24 is out of support and its `crypto/tls`,
  `net/http` and `encoding/asn1` carry unfixed vulnerabilities
  (GO-2026-6090, GO-2026-6089, GO-2026-5972, GO-2026-5856); binaries from
  v0.1.x were built with it. CI and release builds pin the toolchain
  (`GOTOOLCHAIN=local`) and run `govulncheck`.

### Added
- Proxy detection: `target.proxy` reports explicit proxies (from the
  transport's proxy function) and TLS interception (verified chain without
  Certificate Transparency SCTs), each with a warning. CLI prints a `Proxy`
  line.

- Test matrix (`docs/test-matrix.md`) enforced by `TestMatrix`; end-to-end
  tests for HTTP/1.1 fallback, flow errors, `test_endpoint`, TLS 1.2
  normalisation, upload byte cap, custom RoundTrippers, config timeouts,
  the CLI, and the `nqserver` binary. Nightly `Live` workflow.

### Fixed
- `test_endpoint` dial override no longer rewrites dials to a proxy that
  shares the origin's host.
- Foreign probes over a custom RoundTripper that reuses connections now yield
  unstaged samples instead of failing outright.
- `server.SelfSignedCert` returns the parsed leaf certificate, so it can be
  added to a cert pool.

## [0.1.1] - 2026-08-28

### Added
- Release workflow: tags `vX.Y.Z` build `nq`/`nqserver` for all six
  platforms, publish a GitHub release with the matching changelog section,
  and trigger pkg.go.dev indexing.
- CI and Go Reference badges; "Releasing" section in the README.

### Fixed
- Data race in the loopback integration test's event sink (library unchanged).

## [0.1.0] - 2026-08-27

### Added
- `netquality` library implementing draft-ietf-ippm-responsiveness-09:
  config discovery (`/.well-known/nq`, Apple and Cloudflare documents,
  `test_endpoint`), idle latency with per-stage timings, sequential
  download/upload load phases with the draft's moving-average stability
  algorithm, foreign and self loaded-latency probes, RPM, jitter, and
  per-phase confidence.
- Hard safety limits: `MaxDuration`, `MaxBytes`, `MaxFlows`, context
  cancellation; results flag truncation and its reason.
- `cmd/nq` CLI with human and `--json` output, `--events` progress stream,
  exit codes 0/1/2.
- `cmd/nqserver` reference server (HTTP/2, `--self-signed`).
- Unit, loopback integration and opt-in live (`NQ_LIVE=1`) tests; CI on
  Linux/macOS/Windows plus a six-target cross-compile matrix.

[Unreleased]: https://github.com/korya/netquality/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/korya/netquality/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/korya/netquality/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/korya/netquality/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/korya/netquality/releases/tag/v0.1.0
