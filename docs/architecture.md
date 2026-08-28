# Architecture

`netquality` is a single Go module: a library (root package) that runs the
IETF responsiveness test, two thin binaries on top of it, and a reference
server package used by one of the binaries and by the tests.

## Layers

```
cmd/nq ──────────┐                      cmd/nqserver
                 ▼                            │
        netquality (root)                     ▼
  run.go       orchestration: discover → idle → download → upload
  transport.go per-flow transports, dial override, proxy/chain inspection
  probe.go     foreign/self probes via net/http/httptrace
  load.go      load flows, byte accounting, upload body
  engine/     (internal) pure decisions: see below
  target.go    config discovery and validation
  result.go    public result types + JSON contract
  events.go    progress events
                 │
                 ▼
        server/  draft-compliant handler, self-signed certs (test + nqserver)
        internal/engine — pure, clock-free measurement logic
  engine.go    one Observation per interval → one Decision; probe spacing; final summary
  stability.go draft moving-average criterion, confidence
  stats.go     latency statistics, trimmed mean, RPM
        internal/buildinfo  ldflags version metadata
```

**Library** — the product. Everything observable is a `Result` field or an
`Event`. **`cmd/nq`** — flag parsing, output formatting, exit codes; no
measurement logic. **`cmd/nqserver`** — flag parsing and lifecycle around
`server.Handler`. **`server`** — importable so the integration tests run the
real protocol in-process over TLS + HTTP/2 without a network.

## Key technical assumptions

| Assumption | Consequence |
|---|---|
| One `*http.Transport` per load flow | Each flow is its own TCP/TLS connection (draft requirement); self probes multiplex onto it via HTTP/2. A user-supplied non-`*http.Transport` cannot be cloned, so flows may share connections and the library warns. |
| Fresh connection per foreign/idle probe | `DisableKeepAlives` on a dedicated transport; `httptrace` supplies DNS/connect/TLS/TTFB stages. |
| `DialContext` wrapper sees every connection | It implements `test_endpoint`, records remote/local IPs, and is bypassed by user `DialTLSContext` or custom `RoundTripper`s (documented limitation). |
| The engine is pure | `internal/engine` sees only `Observation`s (elapsed, bytes, flows, probe samples) and returns `Decision`s; it holds no clock, goroutine, or socket, so the same code runs against real transports and, in tests, against recorded series or a simulator. Public latency/confidence types are aliases of engine types. The interval loop in `run.go` is driven by an injectable clock. |
| Cancellation via context only | Flows read bodies until the context ends; the upload body reader stops on context; no goroutine outlives `Run` (INV-4). |
| Byte accounting is client-side | Upload bytes are counted when handed to the transport, so a few MB of HTTP/2 flow-control window may be in flight beyond `MaxBytes`. |
| Interval default 1 s, not the draft's 5 s | A 12 s budget cannot fit four 5 s intervals; see README "Deviations" (INV-6). |

## Technology choices

| Choice | Reason |
|---|---|
| Go, stdlib only, no CGO | Cross-compiles for six OS/arch targets; imported by a proprietary agent (Apache-2.0, INV-5/INV-8). |
| `net/http` + `httptrace` rather than raw sockets | Gets HTTP/2, ALPN, proxies, and per-stage timings for free; TCP RTT via `TCP_INFO` is not portable. |
| In-process `httptest` + `server.Handler` for tests | Real TLS/HTTP/2 end to end on all three CI OSes; no mocks. |
| `docs/test-matrix.md` enforced by `TestMatrix` | Coverage of use cases is a document, gated in both directions, instead of a percentage. |
| Pinned toolchain in CI, minimum in `go.mod` | Reproducible `govulncheck`; consumers not forced onto a patch version (INV-9). |

## Intentionally simple for now

No HTTP/3 (draft says MAY; needs `quic-go`), no packet loss (TURN/UDP), no
PAC evaluation, no interface-name resolution for `local_ips`, no scheduling or
persistence — the consuming agent owns those.

## Possible future paths

- HTTP/3 via a build-tagged optional dependency if a target requires it.
- Typed `ErrServerBusy` carrying `Retry-After` for fleet-scale scheduling.
- Rate-adaptive byte cap so gigabit links can still reach four intervals.
