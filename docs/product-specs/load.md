# Load phases

Working-conditions measurement per direction: flow ramp, stability, RPM.
Parameters live in `StabilityParams`; defaults follow draft-09 except the
interval (see INV-6 and README "Deviations").

Prefix: `LOAD`.

---

### LOAD-1: Sequential directions
Download runs before upload, never concurrently. `Directions` selects
`Both` (default), `Download`, or `Upload`; a direction not selected is
absent from the result (INV-3).

### LOAD-2: Independent connections
Each load flow owns its own transport connection. HTTP/2 is attempted first;
the negotiated protocol is recorded per direction and on the target.

### LOAD-3: Flow ramp
A phase starts with `InitialFlows` (default 1) and adds `FlowIncrement`
(default 1) at every interval (default 1 s) until `MaxFlows` (default 16).

### LOAD-4: Goodput stability
Per interval, goodput is bytes moved in the interval ×8 / interval length. The
moving average spans the last `MovingAverageDistance` (default 4) intervals.
Throughput is stable when the standard deviation of the last four moving
averages is below `StdDevTolerance` (default 5 %) of the current one.

### LOAD-5: Responsiveness stability
Once throughput is stable, responsiveness (LOAD-6) is computed each interval
over the last `MovingAverageDistance` intervals of samples and tracked with the
same criterion. When it is stable the phase ends early.

### LOAD-6: RPM formula
`foreign_rpm = 60000 / mean(TM(tcp), TM(tls_per_rtt), TM(http))` over foreign
samples, or `60000 / mean(TM(tcp), TM(http))` without TLS; `self_rpm =
60000 / TM(http_self)`; `rpm` is their mean, or whichever exists. `TM` is the
single-sided trimmed mean at `TrimmedMeanPercent` (default 95).

### LOAD-7: HTTP/1.1 fallback
If the server negotiates HTTP/1.1, load still runs, self probes are skipped,
`rpm` equals `foreign_rpm`, and a warning states it.

### LOAD-8: Confidence
Each series reports `high` when stable, `medium` after at least
`MovingAverageDistance` intervals without stability, else `low`.
Responsiveness confidence is `low` whenever throughput never stabilised.

### LOAD-9: Flow errors
A load request that fails for a reason other than cancellation aborts the
phase with `reason=flow_error` and a warning; everything measured so far in
that direction and in earlier phases is kept. If no interval had completed,
`Run` additionally returns an error carrying the failure (e.g. the HTTP
status) alongside the partial result; only discovery failures yield no
result at all.

### LOAD-10: Finite server objects
A large download that ends cleanly is re-requested on the same connection so
load continues.

### LOAD-11: Throughput figures
`throughput_bps` is the final moving average; `peak_throughput_bps` the best
single interval; `mean_throughput_bps` bytes×8/duration over the whole phase
and the value used for `throughput_bps` when no interval completed.
