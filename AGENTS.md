# netquality — guide for coding agents

`netquality` measures a network path's download and upload capacity, idle and
loaded latency, jitter, and responsiveness (RPM) by running the IETF
"Responsiveness under Working Conditions" test
(draft-ietf-ippm-responsiveness-09) against Apple's, Cloudflare's, or a
self-hosted server. It ships as three things: a Go library, the `nq` client
binary, and the `nqserver` reference server. All three are supported on
Windows, macOS and Linux (amd64 and arm64) — supported as *measurement*
targets, not merely as build targets, so a defect that only distorts numbers
on one platform is a defect.

Two traits define it, and they are principles in their own right rather than
any one consumer's requirements: every run is bounded by time, bytes, and
connections before it starts, and it sends nothing but the test itself. They
exist because the library runs on other people's machines and networks —
embedded in a desktop diagnostics agent, a CLI on a laptop, a probe in CI —
and none of those can afford a measurement that costs an unbounded amount or
phones home.

## Tech stack

| Concern | Choice |
|---|---|
| Language | Go ≥ 1.26 (`go.mod` minimum); CI/release pinned via workflows, `GOTOOLCHAIN=local` |
| Dependencies | Standard library only, no CGO |
| Module | `github.com/korya/netquality` |
| Build/test | `go build ./...`, `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...` |
| Lint config | `.golangci.yml` (standard set; `fmt.Fprint*` and `Body.Close` excluded from errcheck) |
| CI | GitHub Actions: lint + govulncheck, tests on Linux/macOS/Windows, minimum-Go job, 6-target cross-compile; nightly `Live`; tag-triggered `Release` |
| Licence | Apache-2.0; no GPL-derived code |

## Architecture in 30 seconds

`Run` discovers the server config → measures idle latency on fresh
connections → loads download, then upload, adding one HTTP/2 connection per
interval while firing foreign (fresh-connection) and self (multiplexed)
probes → declares stability with the draft's moving-average rule → returns a
`Result` where every number states how it was obtained. `cmd/nq` formats it;
`cmd/nqserver` is a reference server; `server/` is that server as a package
so tests run the real protocol in-process.

## Layout

```
.                 library (package netquality): run, transport, probe, load, stability, stats, target, result, events
cmd/nq            CLI
cmd/nqserver      reference server binary
server/           protocol handler + self-signed TLS helpers
internal/buildinfo  version metadata via -ldflags
scripts/          changelog-notes.sh (release notes extraction)
docs/             agent/human documentation (index below)
testdata/config/  config document fixtures (Apple, Cloudflare, draft, SERVER_SPEC)
.github/workflows ci.yml, release.yml, live.yml
```

## Documentation index

| Path | Summary | Load when… |
|---|---|---|
| `README.md` | User-facing: what it measures, library/CLI usage, self-hosting, safety limits, **Deviations from the draft** | Changing public API, CLI, or wire behaviour |
| `docs/README.md` | How `docs/` is organised: spec rules, when to update what, naming | Adding or restructuring documentation |
| `docs/architecture.md` | Layers, key assumptions, tech choices, future paths | Touching structure, transports, concurrency, or dependencies |
| `docs/guidelines.md` | Quality bar, planning checklist, consultation rules, regression protocol, release steps | Starting any non-trivial change; on a regression; cutting a release |
| `docs/product-specs/README.md` | Spec format and prefix index | Adding or editing a spec |
| `docs/product-specs/invariants.md` | `INV-1..10`: bounds, no telemetry, honest numbers, stdlib only, draft precedence, JSON stability, licensing, toolchain, injectability | Any change; check you don't violate one |
| `docs/product-specs/discovery.md` | `DISC`: targets, config validation, `test_endpoint` | Config parsing, targets, dialing |
| `docs/product-specs/latency.md` | `LAT`: idle/foreign/self probes, TLS normalisation, statistics | Probes, `httptrace`, stats |
| `docs/product-specs/load.md` | `LOAD`: flow ramp, stability, RPM, confidence, HTTP/1.1, flow errors | The load loop, stability params |
| `docs/product-specs/limits.md` | `LIM`: caps, cancellation, no retries | Anything touching budgets or contexts |
| `docs/product-specs/proxy.md` | `PROXY`: explicit proxy and TLS-interception detection | Transport/proxy/certificate code |
| `docs/product-specs/result.md` | `RES`: `Result` shape, JSON contract, events | Changing result or event types |
| `docs/product-specs/cli.md` | `CLI`: flags, outputs, exit codes | `cmd/nq` |
| `docs/product-specs/server.md` | `SRV`: endpoints, TLS, lifecycle | `server/`, `cmd/nqserver` |
| `docs/test-matrix.md` | Feature × scenario → test name; enforced by `TestMatrix` | Adding or renaming any test |
| `CHANGELOG.md` | Keep-a-Changelog; release notes are extracted from it | Every user-visible change; releases |

## Process rules

- All changes go through a draft pull request from a `<author>-<slug>`
  branch; never push to `master`. See `docs/guidelines.md` "Submitting
  changes" for commit and PR description rules.
- Lint and vet clean; `go test -race ./...` green on all three OSes. State
  reachable from handlers, flow/probe goroutines, event sinks, or test
  helpers wrapping them is shared: mutex or atomic by default; run
  `-race -count=3` on touched packages before pushing.
- Every new behaviour gets a spec ID, a test, and a matrix row.
- Follow the planning checklist in `docs/guidelines.md`; validate assumptions
  against `HEAD` and with experiments, not memory.
- Behaviour change → consult the spec; structure change → consult
  `docs/architecture.md`; wire change → consult the draft and README
  "Deviations". Conflicts are raised before coding.
- Regression → 5-whys root cause first, fix second. Suspect the network
  before the code: this project's failures are often a sick route.
- Spec and architecture edits are confirmed with the user before commit.
- Spec IDs (`INV-n`, `DISC-n`, `LAT-n`, `LOAD-n`, `LIM-n`, `PROXY-n`,
  `RES-n`, `CLI-n`, `SRV-n`) are stable references; never renumber.
