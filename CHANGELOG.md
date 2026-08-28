# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

[Unreleased]: https://github.com/korya/netquality/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/korya/netquality/releases/tag/v0.1.0
