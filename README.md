# netquality [![CI](https://github.com/korya/netquality/actions/workflows/ci.yml/badge.svg)](https://github.com/korya/netquality/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/korya/netquality.svg)](https://pkg.go.dev/github.com/korya/netquality)

Measure the **capacity, latency and responsiveness** of a network path by
running the IETF *Responsiveness under Working Conditions* test
([draft-ietf-ippm-responsiveness-09][draft]) against Apple's, Cloudflare's, or
your own server. Ships as three things: a **Go library**, the **`nq`** client,
and **`nqserver`**, a reference server you can host yourself.

- **Windows, macOS and Linux** on amd64 and arm64, supported as measurement
  targets rather than just build targets: where a platform's defaults would
  distort a number, the library works around them.
- Standard library only, no CGO, so it adds nothing to your build. Requires
  Go 1.26+ (older lines no longer receive TLS/HTTP security fixes).
- Every run is bounded by **time and connection count** before it starts, and
  by bytes too when you say the link is metered. Every number in the result
  says how it was obtained.
- Sends nothing over the network except the test itself.

```
$ nq --target apple
Target     mensura.cdn-apple.com (HTTP/2.0) 17.253.24.71
Idle       102.2ms median, 131.8ms p80, jitter 32.5ms (5 probes)
Download      178.3 Mbps    365 RPM  loaded 185.2ms median, 512.6ms p99  [8 flows, high/high confidence]
Upload        125.9 Mbps    311 RPM  (>= 121.4 Mbps, <= 330 RPM over 4s)  loaded 160.7ms median, 471.0ms p99  [8 flows, medium/low confidence, TRUNCATED: duration_cap]
Cost       370.5 MB moved in 21.0s
```

## What it measures

1. **Discovery.** Fetches the server's JSON config (`/.well-known/nq` or a
   vendor path) to learn the small-download, large-download and upload URLs.
2. **Idle latency.** `IdleProbes` sequential GETs of the small resource
   (1 byte in the draft; up to 10 accepted for Cloudflare compatibility),
   each on a **fresh connection**, so a sample includes DNS + TCP + TLS + HTTP.
   Per-stage medians are reported via `net/http/httptrace`.
3. **Download under load**, then **upload under load** (sequentially, so the
   two loaded-latency figures are distinct). Each phase:
   - opens one HTTP/2 connection to the large resource and adds one more every
     interval (1 s) up to `MaxFlows`;
   - meanwhile fires *foreign* probes (fresh connections) and *self* probes
     (multiplexed on a random load connection), interleaved, at up to
     `MaxProbesPerSecond` and never more than 5 % of measured capacity;
   - declares throughput stable when the standard deviation of the last four
     moving averages is under 5 % of the current one, then does the same for
     responsiveness and stops.
4. **Responsiveness (RPM).** "Round trips per minute", `60000 / RTT_ms`.
   Following the draft:
   `foreign = 60000 / mean(TM(tcp), TM(tls per RTT), TM(http))`,
   `self = 60000 / TM(http_on_loaded_connection)`, `RPM = (foreign+self)/2`,
   where `TM` is the single-sided trimmed mean at the 95th percentile over the
   last four intervals. Roughly: < 300 RPM poor, > 1000 good, > 6000 excellent.
5. **Jitter.** Mean absolute deviation of the samples from their mean,
   reported for idle and loaded sets. Percentiles (`p80`/`p90`/`p95`/`p99`)
   appear only when there are enough samples for them to differ from the
   maximum (5/10/20/100 under nearest rank), so the default five idle probes
   yield `p80`, never a fake `p95`.
6. **Cost.** Bytes moved and wall time per phase.

## Library

```go
import "github.com/korya/netquality"

res, err := netquality.Run(ctx, netquality.Cloudflare, netquality.Options{
    MaxDuration: 10 * time.Second, // per direction; the budget
    MaxBytes:    100 << 20,        // optional: only on metered links
    MaxFlows:    8,
    IdleTimeout: 5 * time.Second, // budget for all idle probes together
})
if err != nil { /* discovery failed, or ctx cancelled (res is then partial) */ }
fmt.Println(res.Download.RPM, res.Download.ThroughputBPS, res.Download.Truncated)
```

`Result.Target` records the server IPs the flows reached (`resolved_ips`) and
the local source addresses they went out on (`local_ips`), so a stored result
can be tied to the interface or network it was measured on. Both are present
on cancelled partial results too, and both stay in the result if you share it.

Targets: `netquality.Apple`, `netquality.Cloudflare`, `netquality.WellKnown("host:port")`,
or `netquality.Target{ConfigURL: "..."}`.

`Options.Events` takes a `func(Event)` sink for progress bars. Proxies and TLS
settings come through `Options.HTTPClient` (its `*http.Transport` is cloned per
flow so each flow owns a connection). `Options.Logger` accepts a `*slog.Logger`.

`Result` marshals to JSON with stable snake_case names; the CLI's `--json`
output is exactly that struct. `schema_version` (currently 1) is its first
field: it changes only when a field is renamed, removed, retyped, or changes
meaning, never for additions, so stored documents stay interpretable.
Directions that did not run are omitted, not zero. Each `DirectionResult`
carries `truncated`, `reason`
(`bytes_cap` | `duration_cap` | `cancelled` | `flow_error`) and the draft's
`throughput_confidence` / `responsiveness_confidence` (`low` | `medium` | `high`).
Whenever four consecutive intervals agreed, a direction also carries
`throughput_lower_bound_bps`, with its `lower_bound_window` and
`rpm_upper_bound` (LOAD-13). That figure is deliberately conservative: it
holds even when the estimate did not converge.

## CLI

```
nq                                   # Cloudflare, both directions
nq --target apple
nq --well-known nq.example.com:8443 [--insecure]
nq --config-url https://host/path/config
nq --download-only | --upload-only
nq --max-duration 8s --max-flows 8          # time is the budget
nq --idle-timeout 5s                       # cap the whole idle measurement phase
nq --max-bytes 100MB                         # metered link: add a byte cap
nq --json                            # Result as one JSON document on stdout
nq --events                          # JSON-lines progress on stderr
nq version
```

Exit codes: `0` ok, `1` the test failed or was cancelled, `2` bad usage.

Build with version info:

```
go build -ldflags "-X github.com/korya/netquality/internal/buildinfo.Version=v0.1.0 \
  -X github.com/korya/netquality/internal/buildinfo.Commit=$(git rev-parse HEAD)" ./cmd/nq
```

## Self-hosting `nqserver`

```
go run ./cmd/nqserver --self-signed --listen :8443
nq --well-known localhost:8443 --insecure
```

For real deployments pass `--cert/--key`; `--base-url` sets the advertised URL
prefix when behind a load balancer, `--test-endpoint` advertises a specific
host. The server needs HTTP/2 end to end and must not compress or redirect.

**Access control.** With a real certificate the server refuses to start
anonymously; give it a token and give the client the same one:

```
NQSERVER_AUTH_TOKEN=s3cret nqserver --cert c.pem --key k.pem
NQ_AUTH_TOKEN=s3cret nq --well-known nq.example.com
```

(`--auth-token` works on both; the environment keeps the secret out of `ps`.)
Every endpoint, config included, answers `401` without the token. Library
callers set `Options.Header`. `--allow-anonymous` opts out explicitly;
`--self-signed` implies it for local development.

**Load limits** protect egress without biasing measurements. They gate whether
a request may *start*, and never slow down one that has already begun:

| Flag | Default | Effect |
|---|---|---|
| `--client-bytes` / `--client-window` | 8 GiB / 10 min (unlimited with `--self-signed`) | token-bucket credit per client IP or signed subject; refusal gets `429` + `Retry-After` |
| `--client-concurrency` | 32 | active large/download and upload requests per budget identity; nonpositive selects 32; disabled when the byte budget is disabled |
| `--upload-size` | 16 GiB | bytes accepted by one upload |
| `--large-size` | 8 GiB | bytes served by one download |
| `--max-connections` | 256 | extra connections wait in the accept queue |
| `--idle-timeout` | 2 min | closes a connection with no request in flight; HTTP/2 peers are pinged after 30 s of silence; transfers are never cut |

The server checks credit and a free slot atomically before a transfer starts,
then charges the actual payload bytes and releases the slot when the handler
returns, including after cancellation or an I/O error; a panic charges the
request cap. Config and small probe requests never take a slot.
`--client-bytes -1` disables byte budgeting and admission slots together, and
an explicit `--client-concurrency` then warns that it is ignored, `--self-signed`
included. To keep the concurrency bound without practical byte refusals, set
`--client-bytes` high rather than negative.

**It is not a strict quota.** With concurrency C and the larger request cap R,
admitted payload can overshoot available credit by C × R: **512 GiB on the
defaults** (32 × 16 GiB), on top of the 8 GiB bucket and whatever it refills.
HTTP and TLS overhead, transport buffering and body cleanup fall outside the
accounting entirely. `Retry-After` is a hint, not a reservation: one second
for a concurrency refusal, byte debt rounded up to seconds and saturated when
the debt is very large.

**Stalled requests can deny a shared identity indefinitely.** Slots have no
expiry, so an upload that sends no body, a download whose receiver stops
reading, or either stalling mid-transfer holds its slot until the handler
exits, blocking large transfers for everyone on that identity across any
number of refill windows. Idle timeouts do not reclaim active requests, and
HTTP/2 pings cannot distinguish a stalled body from a peer that still answers
pings.

Size `--client-concurrency` for one identity's simultaneous load flows plus
teardown overlap, and `--max-connections` for every client's load connections,
fresh probes and teardown. HTTP/2 multiplexes, so the two count different
things: one identity can exhaust its 32 slots long before the server reaches
256 connections, and raising the global limit adds no per-client slots. The
default leaves headroom for a client's 16 flows; raise it for more flows or
more clients behind one address, and expect `429` with `flow_error` if it is
too low. Library callers set `server.Options.MaxClientConcurrency`. Behind a
load balancer every client keys to the balancer's address, because the server
deliberately does not trust `X-Forwarded-For`. Only a signed `sub` separates
devices there; a shared bearer token leaves them all on one IP-keyed budget.

**Signed URLs: no secret on clients.** Your backend serves the config
document with test URLs it has signed; `nqserver` verifies them and the
laptop never holds a reusable credential:

```
nqserver sign --new-key                       # once: a 32-byte key, keep it on the backend and the server
NQSERVER_SIGNING_KEY=<key> nqserver --cert c.pem --key k.pem
nqserver sign --key <key> --ttl 10m --sub laptop-7 https://nq.example.com/nq/small \
    https://nq.example.com/nq/large https://nq.example.com/nq/upload
```

Put the three signed URLs in a config document served by your backend and
point the client at it (`Target{ConfigURL: "https://backend/nq-config"}`).
The client needs no flags. The signature covers only the path, `exp` and `sub`:
`sig = base64url(HMAC-SHA256(key, path + "\n" + exp + "\n" + sub))`, so any
language can issue one, and unsigned parameters are deliberately unprotected
(never let a server trust them). `sub` keys the per-client budget, so ten
laptops behind one NAT get ten budgets. Repeat `--signing-key` to rotate; Go
backends can call `server.SignURL`. Validity is `exp` plus 30 s of leeway and
at most 24 h, so keep the two clocks in sync: a server running behind the
issuer refuses everything as "issued too far ahead". The encoding rules that
bite in practice, including which form of the path to sign and why `sub` must
be percent-encoded, are in [SRV-10](docs/product-specs/server.md#srv-10-signed-urls).

**mTLS** works today without a flag: wrap `server.Handler` in your own
`http.Server` with `TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert`
and `ClientCAs`, and give the client its certificate via
`Options.HTTPClient.Transport.TLSClientConfig.Certificates`. A `--client-ca`
flag is tracked in issue #10.

## Proxies

Corporate laptops often sit behind proxies, in which case the test measures
the laptop→proxy leg, not the path to the server. The result says so rather
than reporting plausible-but-wrong numbers:

- **Explicit proxy** (`HTTPS_PROXY`, PAC, or `Transport.Proxy` on the client
  you pass in): `target.proxy.explicit=true` with the proxy `url`
  (credentials stripped). HTTP/2 still works through CONNECT tunnels.
  `test_endpoint` cannot be honoured, and a warning says so.
- **TLS interception** (Zscaler, Netskope, and similar): the certificate chain
  verifies against the corporate root, but the leaf carries no Certificate
  Transparency SCTs, which every publicly trusted certificate has had since
  2018. `target.proxy.tls_interception=true` with the `issuer` and a `reason`.
  A self-hosted `nqserver` behind a private CA triggers the same flag, since
  the trust situation is identical; the wording says "proxy or private CA".
  `--insecure` skips verification and therefore never triggers it.

Both cases add a warning. Confidence scores are unaffected: the algorithm
converged on a real measurement, just of a shorter path. Not detected: proxies
that re-issue publicly trusted certificates (not possible without a CA
compromise) and transparent TCP-level proxies that pass TLS through untouched
(those are not altering the measurement).

## Safety limits

| Limit | Default | Effect |
|---|---|---|
| `ConfigTimeout` | 10 s | bounds discovery; failure returns no result |
| `IdleTimeout` | 10 s | bounds all idle probes together; keeps completed samples, warns, then proceeds to load |
| `MaxDuration` | 12 s per direction | the budget: phase ends; if not yet stable → `truncated`, `reason=duration_cap`. Cost ≤ rate × 12 s |
| `MaxBytes` | **none** (opt-in) | set on metered links; phase ends → `reason=bytes_cap` |
| `MaxFlows` | 16 | never more concurrent load connections |
| Small response body | 1-10 bytes | reject empty/oversized responses; read at most 11 bytes to detect overflow; discard invalid samples and warn |
| `ctx` cancellation | n/a | all flows stop within ~200 ms; partial result, `cancelled=true` |

The combined phase budget is `ConfigTimeout + IdleTimeout + N × MaxDuration`,
where N is the selected direction count; omit `IdleTimeout` when idle is
skipped. Defaults total 44 s for both directions, 32 s for one, or 34 s for
both with idle skipped. Zero or negative duration options select defaults, so
they cannot disable the bounds, and an earlier caller deadline stops the run
while keeping what was already measured. `IdleTimeout` is a flat cap that does
not grow with `IdleProbes`, so a large sample set or a healthy slow path may
need a larger one to reach the requested percentiles; the warning it produces
means the budget ran out, not that the connection is faulty. The server flag
of the same name is unrelated: it bounds quiet connections between requests.

Return time also includes local orchestration and prompt teardown. Supplied
transports, dialers, body closers, event sinks, and log handlers must honor
cancellation where applicable and return promptly; the library cannot
forcibly stop caller code. Runner goroutines join and owned sockets close
before return. A custom TLS dialer still executing at teardown has its
connection closed when it hands it over. There are no retries or telemetry.

`MaxBytes` is not an exact wire-byte limit. Probe cost is estimated (see
Deviations) and charged even for failed loaded attempts, idle and discovery
sit outside the per-direction totals, and headers, TLS, read-ahead, in-flight
work and cancellation can all exceed the accounted budget.

A server whose small response leaves the 1-10 byte range stops being usable
for latency probing. Load still collects capacity within the configured
budgets even when every probe fails, with loaded latency absent and RPM zero,
so `MaxBytes` and `MaxDuration` are what bound that cost. Invalid-size
warnings are capped at one per phase and probe kind.
[Dated compatibility checks](testdata/config/README.md) record the observed
Apple and Cloudflare response sizes and how to repeat the small GETs.

## Deviations from the draft

| Item | Draft | Here | Why |
|---|---|---|---|
| Interval (ID) | 5 s | **1 s** | 4 intervals must complete before stability can be declared; with the 12 s per-direction budget a 5 s interval could never stabilise. Earlier drafts and shipping tools use 1 s. Configurable via `Stability.Interval`. |
| Time budget | "implementations may" limit | mandatory discovery, idle, and per-direction time caps; `MaxBytes` opt-in | Runs on other people's machines and networks; a time bound makes cost proportional to the link instead of unbounded. |
| Byte cap default | (handoff spec: 250 MB) | none | A fixed byte cap starves fast links of the intervals a confident RPM needs (≈ 8 × rate); the caller knows which networks are metered, the library cannot. |
| Flow error | abort the test | abort the **phase**, report `reason=flow_error`, keep other results | Partial data with a flag beats none. |
| Self probes on HTTP/1.1 | use TCP RTT estimate | omitted; RPM from foreign probes only, warning recorded | TCP_INFO is not portable in pure Go. |
| Server admission | successful load endpoints return 200 | per-client byte credit and concurrent-request slots may refuse new load requests with 429 | Bounds admitted work without throttling transfers already running; probes are exempt. |
| Flow addition | one per interval | **doubling** each interval while a step gains ≥ 10 % goodput, up to `MaxFlows` | Reaches saturation in ≤ 5 intervals instead of 16, so a 10 Gbps or high-RTT link still settles inside the 12 s budget; a slow link stops after one exploratory flow. |
| Responsiveness tracking | after goodput stability | from the end of the ramp; stability judged on the windowed values, not on averages of them | Removes 3-4 s of latency from every run; the phase still ends only with both series stable. |
| Upload byte accounting | not specified | intervals inflated by the HTTP/2 send window of new flows are excluded | Bytes are counted when the transport takes them; on a 20 Mbps link the 4 MiB credit otherwise reports 53 Mbps with high confidence. |
| Capacity change | not specified | a > 25 % goodput drop restarts stability tracking | The draft averages across the change. |
| Responsiveness window | last MAD intervals | every sample since throughput became stable (`loaded_window`) | Foreign probes are sparse (a TLS handshake each); a fixed 4-tick window could hold self samples and no foreign ones, which read as "no fresh connection ever succeeded". Stability is still judged on the draft's window. |
| Probe byte accounting | not specified | foreign 5000 B, self 1000 B (draft's estimates) | Counted against `MaxBytes` and the 5 % capacity rule. |
| Small response size | 1 byte | complete nonempty bodies up to 10 bytes accepted; fixed ceiling, no override | Preserve Cloudflare's advertised ten-byte probe; reject empty and larger bodies before they become latency samples. |
| Config `version` | must be `1` | `1` or `"1"` accepted | Lenient on the wire, strict on everything else (duplicates, hosts, scheme). |
| Config field names | `*_download_url`, `upload_url` | also accepts Apple/Cloudflare `*_https_*` names, preferring them | Interop with deployed servers. |
| Cloudflare target | `mach` hardcodes `h3.speed.cloudflare.com` URLs | uses `aim.cloudflare.com/responsiveness/api/v1/config`, which returns the same URLs | Keeps discovery uniform. |

Other constants: `IdleProbes=5` (enough for a median, cheap), `ConfigTimeout=10s`, `IdleTimeout=10s`,
in-flight probe cap 64 (bounds goroutines on high-RTT links), TLS handshake
normalised to 1 RTT for TLS 1.3 and 2 for TLS 1.2.

## Testing

```
go test ./...            # unit + end-to-end against an in-process nqserver
NQ_LIVE=1 go test -run TestLive -v .   # hits Apple and Cloudflare
```

[`docs/test-matrix.md`](docs/test-matrix.md) maps every feature and use case
to the test that exercises it; `TestMatrix` fails if a row names a missing
test or a test is missing from the matrix. Behaviours only observable on real
networks (stability under bufferbloat, probe throttling on slow links) run in
the nightly `Live` workflow, which never blocks merges.

## Releasing

1. Move the `[Unreleased]` items in `CHANGELOG.md` under a new `## [X.Y.Z] - date` heading.
2. Commit, then `git tag -a vX.Y.Z -m "netquality vX.Y.Z" && git push origin master vX.Y.Z`.

The `Release` workflow runs the tests, builds `nq` and `nqserver` for all six
platforms, and publishes a GitHub release whose notes are that changelog
section. It fails if the section is missing.

## Licence

Apache-2.0. Written from the draft and Apple's MIT-licensed `SERVER_SPEC.md`;
no code from GPL implementations (e.g. `goresponsiveness`) is included.

[draft]: https://datatracker.ietf.org/doc/draft-ietf-ippm-responsiveness/
