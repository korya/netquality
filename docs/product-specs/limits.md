# Safety limits

Bounds that hold for every run (INV-1) and how hitting one is reported.

Prefix: `LIM`.

---

### LIM-1: Duration cap
`MaxDuration` (default 12 s) bounds each direction's load phase. Reaching it
before both series are stable ends the phase with `truncated=true,
reason=duration_cap` and a warning.

### LIM-2: Byte cap (opt-in)
There is no byte cap by default: time is the budget, so a run costs at most
link rate × `MaxDuration` per direction and can reach a confident result at
any speed. A caller on a metered link sets `MaxBytes` (> 0), which then
bounds bytes moved per direction, counting load payload plus a fixed estimate
per probe (5000 B foreign, 1000 B self); reaching it ends the phase with
`reason=bytes_cap` and a warning. `MaxBytes` ≤ 0 means unlimited.

### LIM-3: Flow cap
`MaxFlows` (default 16) is never exceeded in any direction.

### LIM-4: Cancellation
Cancelling the context stops all flows and probes within about 200 ms.
`Run` returns the partial `Result` with `cancelled=true`, the current
direction marked `reason=cancelled`, together with the context error.
An earlier caller deadline has the same effect; `duration_cap` identifies
the direction's own time cap. An independently recorded stop (such as a
byte cap or flow error) retains its reason. Completed idle samples survive
cancellation, and directions that have not started remain absent.

### LIM-5: Cancelled results keep the network identity
`target.resolved_ips` and `target.local_ips` are populated on cancelled and
truncated results alike.

### LIM-6: No retries
A failed discovery, probe, or flow is never retried; one run is one bounded
amount of traffic.

### LIM-7: Zero and negative options
Zero values in `Options` select the defaults; a negative `IdleProbes` skips
idle probing. Zero `StabilityParams` fields select the draft defaults.
Non-positive `ConfigTimeout`, `IdleTimeout`, and `MaxDuration` select their
defaults; they never disable the time bounds.

### LIM-8: Idle phase cap
`IdleTimeout` (default 10 s) bounds the entire idle measurement phase,
regardless of `IdleProbes`. On expiry, completed samples are retained and a
warning names the timeout and successful/requested probe counts. An idle
timeout alone does not cancel the run: selected load phases still execute
with their own budgets. An earlier caller cancellation or deadline stops
the run instead (LIM-4). Skipping idle measurement creates no idle deadline.

### LIM-9: Combined phase budget
Before network work begins, the combined phase budget is `ConfigTimeout`
plus `IdleTimeout` when idle is enabled, plus `MaxDuration` for each selected
direction. Defaults total 44 s for both directions, 32 s for one, or 34 s
for both with idle skipped. An earlier parent deadline takes precedence.
Return time also includes local orchestration and prompt teardown: this is
not an unconditional wall-clock deadline for a descheduled process or
blocking caller code. Supplied transports, dialers, body closers, event
sinks and log handlers must cooperate with cancellation and return promptly;
they cannot be forcibly stopped by the library (INV-4).
