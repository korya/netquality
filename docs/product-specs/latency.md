# Latency

Idle latency before load, and the foreign/self probes taken under load.
Statistics conventions apply to all latency sets.

Prefix: `LAT`.

---

### LAT-1: Idle probes
Before any load, `IdleProbes` (default 5) sequential GETs of the small
resource run, each on a brand-new connection. A negative `IdleProbes` skips
the phase and `Result.Idle` is absent. Failed probes are dropped; if none
succeed the phase yields no result and a warning.

### LAT-2: Per-stage timings
Every fresh-connection sample records DNS, TCP connect, TLS handshake,
time-to-first-byte, and request-to-full-response durations. Stats over such
samples expose the medians of each stage.

### LAT-3: TLS handshake normalisation
The TLS stage is additionally reported per round trip: divided by 1 for
TLS 1.3 and by 2 for TLS 1.2, matching the draft's `tls_f` definition.

### LAT-4: Foreign probes under load
While a direction is loaded, probes on brand-new connections (as in LAT-1)
run interleaved with self probes. Their samples form `loaded.foreign`.

### LAT-5: Self probes under load
Self probes GET the small resource multiplexed on a randomly chosen active
HTTP/2 load connection and record request-to-full-response time. They form
`loaded.self`. On HTTP/1.1 they are not attempted (see LOAD-7).

### LAT-6: Probe rate
Probes are launched alternately (foreign, self, …) at most `MaxProbesPerSecond`
(default 100) per second, stretched so probe traffic stays under
`ProbeCapacityPercent` (default 5 %) of the current goodput estimate, with at
most 64 in flight. Probe bytes count against `MaxBytes` (see LIM-2).

### LAT-7: Statistics
Every latency set reports sample count, min, median, mean, max, and jitter
defined as the mean absolute deviation from the mean. Percentiles use the
nearest-rank method and appear only when the sample count makes them distinct
from the maximum: `p80` from 5 samples, `p90` from 10, `p95` from 20, `p99`
from 100. A percentile field never holds a lower percentile than its name;
an absent field means too few samples. With the default 5 idle probes the
idle set reports `p80`; loaded sets usually report all four.

### LAT-8: Combined loaded latency
`loaded.combined` merges foreign and self samples using each probe's
request-to-full-response time, so both kinds measure the same thing.

### LAT-9: Reused connections
If the supplied transport reuses a connection for a foreign or idle probe
(possible only with a custom `RoundTripper`), the sample is kept but carries
no stage timings, and the stats omit stage medians.
