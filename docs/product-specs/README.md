# Product specs

Behavioural specifications of `netquality`. Each spec is an assertion precise
enough that a tester can derive a check from its body alone.

## Format

- **Behavioural.** What the system does, not how. No function, type, or file
  names in spec bodies.
- **Testable as written.** No separate "testable" annotations; if a body is
  not testable, rewrite the body.
- **Identified.** `### PREFIX-N: Title`. The prefix is fixed per file; numbers
  are never reused. Deleted specs leave holes.

**Feature specs** describe observable behaviour of one area. **Invariants**
(`invariants.md`, prefix `INV`) describe system-wide properties upheld by
review, architecture, and process; they need not be testable in isolation.

## Files

| File | Prefix | Covers |
|---|---|---|
| `invariants.md` | `INV` | Bounds, no telemetry, honest numbers, stdlib only, draft precedence, JSON stability, licensing, toolchain |
| `discovery.md` | `DISC` | Targets, config document validation, `test_endpoint` |
| `latency.md` | `LAT` | Idle probes, foreign/self probes, TLS normalisation, statistics |
| `load.md` | `LOAD` | Flow ramp, stability criterion, RPM, confidence, HTTP/1.1, flow errors |
| `limits.md` | `LIM` | Duration/byte/flow caps, cancellation, no retries |
| `proxy.md` | `PROXY` | Explicit proxy and TLS-interception detection |
| `result.md` | `RES` | `Result` shape, JSON contract, events |
| `cli.md` | `CLI` | `nq` flags, outputs, exit codes |
| `server.md` | `SRV` | `nqserver` endpoints, TLS, lifecycle |

The wire protocol itself is specified by
[draft-ietf-ippm-responsiveness](https://datatracker.ietf.org/doc/draft-ietf-ippm-responsiveness/);
these specs cover this implementation's behaviour on top of it. Deviations
from the draft are listed in the README and governed by INV-6.

Test coverage per spec area is tracked in [`../test-matrix.md`](../test-matrix.md).
