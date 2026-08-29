# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed
- **Ramp-and-hold algorithm** (#20). Flows are added in doubling steps and the
  ramp stops when a step gains less than 10 % goodput; responsiveness is
  tracked from the end of the ramp and judged on its windowed values; a
  goodput drop of more than 25 % restarts goodput tracking; upload intervals
  inflated by the HTTP/2 send window of new flows are excluded. Every
  simulated scenario now converges with high confidence inside the 12 s
  budget (10 Gbps at 150 ms RTT, CDN per-flow caps, shaper bursts, a
  capacity change mid-run), links ≤ 100 Mbps finish 1–3 s sooner, and a
  20 Mbps upload reports 20 Mbps instead of 53. New `StabilityParams`
  fields: `SendBufferBytes`, `RampGainTolerance`, `ChangeTolerance`;
  `DefaultUploadSendBuffer`. `FlowIncrement` is now the floor of a step.
- Engine summaries carry a sustained lower bound on throughput — the lowest
  goodput of the latest four consecutive measured intervals within tolerance
  of their mean — and the matching RPM upper bound (not yet in `Result`;
  see #20).

### Fixed
- Throughput no longer measures the test's own probe traffic. The fixed
  per-probe byte estimate (5000 B foreign, 1000 B self) was added to the same
  counter the engine reads as goodput, so a stalled or very slow path
  reported hundreds of kbps that were purely probes — and, because probe
  spacing is derived from that estimate, the client throttled itself to about
  one probe per second on exactly the paths that most need probing. Probe
  cost still counts against `MaxBytes` and `bytes` (LIM-2); `throughput_bps`,
  `peak_throughput_bps` and `mean_throughput_bps` now count load-flow bytes
  only (LOAD-4, LOAD-11).
- Data race in the load phase: the reason a phase stopped was read on the
  phase goroutine before the flow and probe goroutines were joined, while any
  of them could still be writing it.

## [0.3.0] - 2026-08-28

### Changed
- **No byte cap by default.** `MaxBytes` defaults to 0 (unlimited) and
  `nq --max-bytes` is omitted unless the link is metered; `MaxDuration`
  (12 s per direction) is the budget, so cost is at most link rate × 12 s and
  a confident result is reachable at any speed. Previously a 250 MiB cap
  truncated every run above ~250 Mbps with low confidence (#3). Slow links
  are unaffected. Set `MaxBytes` on metered links for the old behaviour.
- Latency percentiles are honest about sample size: `p95_ns` is present only
  from 20 samples, and `p80_ns` (from 5), `p90_ns` (from 10) and `p99_ns`
  (from 100) are added. Previously `p95_ns` of the five idle probes was
  always the maximum (#4). The CLI prints the highest percentile available.
- A load phase that fails before completing an interval no longer discards
  the whole result: `Run` returns the partial `Result` (that direction
  flagged `flow_error`, earlier phases intact) together with the error. Only
  discovery failures return no result.
- Signed URLs: `sig` accepted in raw or padded, URL-safe or standard base64.
- `nqserver` refuses to serve anonymously with a real certificate unless
  `--allow-anonymous`; `--self-signed` implies it.
- Internal: the per-interval measurement logic now lives in a pure,
  clock-free `internal/engine` package driven by observations, so it can be
  tested against recorded and simulated links. No behaviour or JSON change;
  public latency types are aliases of the engine's.

### Added
- `schema_version` (1) as the first field of `Result`, bumped only when a
  field is renamed, removed, retyped, or changes meaning (#2).
- Regression guards: leak check across mixed runs, wire-contract test,
  repeated-run isolation, JSON schema golden and v0.2.1 document fixture,
  fuzz targets for the four parsers (with a CI job), and a per-scenario cost
  ledger for the algorithm matrix.
- End-to-end tests for real-world failure modes (upload rejected mid-stream,
  stalled body, redirects, dead hosts, upload cancellation/HTTP/1.1), IPv6
  loopback, full ramp to 16 flows, JSON round-trip, CLI edge flags, and the
  signed-URL encoding/canonicalisation edge cases.
- `nqserver --signing-key`: the test endpoints accept HMAC-SHA256-signed,
  expiring URLs (`exp`, optional `sub`, `sig`) minted by a backend, so
  clients hold no credential; `sub` keys the per-client budget. `nqserver
  sign` mints keys and URLs; `server.SignURL` for Go issuers (#1, plan B).
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

### Fixed
- Every load phase leaked one HTTP/2 connection and its read/write
  goroutines when a stream was still winding down at teardown (present since
  v0.1.0); the config fetch also left a keep-alive connection in the caller's
  transport pool. All library-dialled connections are now tracked and closed
  when `Run` returns (INV-4).
- `nq --max-bytes` rejected 0 but accepted sub-byte values that rounded to 0
  and silently meant the default.

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

[Unreleased]: https://github.com/korya/netquality/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/korya/netquality/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/korya/netquality/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/korya/netquality/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/korya/netquality/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/korya/netquality/releases/tag/v0.1.0
